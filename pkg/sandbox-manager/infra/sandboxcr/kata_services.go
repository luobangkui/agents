/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/
package sandboxcr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	coreclient "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"

	"github.com/openkruise/agents/pkg/auditruntime/kataproof"
	"github.com/openkruise/agents/pkg/sandbox-manager/config"
	managererrors "github.com/openkruise/agents/pkg/sandbox-manager/errors"
	"github.com/openkruise/agents/pkg/sandbox-manager/infra"
	corev1 "k8s.io/api/core/v1"
)

const strictKataProfile = "kata-pids32-guest-proof-v1"

type kataRuntimeState struct {
	Target    infra.ServiceTarget `json:"target"`
	Phase     string              `json:"phase"`
	Reclaimed bool                `json:"reclaimed"`
	Stops     map[string]*struct {
		State   string          `json:"state"`
		Receipt json.RawMessage `json:"receipt"`
	} `json:"stops"`
}

func (i *Infra) kataBinding(sbx infra.Sandbox, p *corev1.Pod) (config.KataServiceBinding, bool) {
	for _, b := range i.kataServices {
		if b.Namespace == sbx.GetNamespace() && b.SandboxUID == string(sbx.GetUID()) && b.PodUID == string(p.UID) {
			return b, true
		}
	}
	return config.KataServiceBinding{}, false
}

// The trusted supervisor owns the local token and runtime journal. Neither an
// endpoint, token, execution ID nor immutable runtime target comes from clients.
func (i *Infra) kataCall(ctx context.Context, sbx infra.Sandbox, p *corev1.Pod, b config.KataServiceBinding, kind string, a infra.ServiceAction, expected *infra.ServiceTarget, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	payload := map[string]any{"id": b.ExecutionID, "sandboxUID": b.SandboxUID, "podUID": b.PodUID, "kind": kind, "action": a, "expected": expected}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if i.serviceConfig == nil {
		return fmt.Errorf("strict runtime transport not configured")
	}
	core, err := coreclient.NewForConfig(i.serviceConfig)
	if err != nil {
		return err
	}
	u := core.RESTClient().Post().Namespace(p.Namespace).Resource("pods").Name(p.Name).SubResource("portforward").URL()
	transport, upgrader, err := spdy.RoundTripperFor(i.serviceConfig)
	if err != nil {
		return err
	}
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, http.MethodPost, u)
	stop, ready := make(chan struct{}), make(chan struct{})
	defer close(stop)
	forward, err := portforward.NewOnAddresses(dialer, []string{"127.0.0.1"}, []string{"0:38123"}, stop, ready, io.Discard, io.Discard)
	if err != nil {
		return err
	}
	result := make(chan error, 1)
	go func() { result <- forward.ForwardPorts() }()
	select {
	case <-ready:
	case err := <-result:
		return fmt.Errorf("strict control forwarding: %w", err)
	case <-ctx.Done():
		return ctx.Err()
	}
	ports, err := forward.GetPorts()
	if err != nil || len(ports) != 1 {
		return fmt.Errorf("strict control port unavailable")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/sandbox-service", ports[0].Local), bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+b.ControlToken)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4*1024*1024+1))
	if err != nil {
		return err
	}
	if len(body) > 4*1024*1024 {
		return fmt.Errorf("strict response exceeds limit")
	}
	if response.StatusCode != 200 {
		return fmt.Errorf("strict Kata operation not confirmed: %s", body)
	}
	if err = json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("invalid strict runtime response: %w", err)
	}
	latest, _, err := i.servicePod(ctx, sbx)
	if err != nil {
		return err
	}
	if latest.UID != p.UID {
		return serviceError(managererrors.ErrorConflict, "supervisor replaced during runtime operation")
	}
	return nil
}
func validateKataPolicy(state kataRuntimeState, id string) error {
	t := state.Target
	if state.Reclaimed || state.Phase != "Running" || t.ExecutionID != id || t.OwnerID == "" || t.InstanceID == "" || t.Containers[serviceNameMain] != id+"-main" || t.Containers["observer"] != id+"-observer" {
		return fmt.Errorf("invalid strict runtime binding")
	}
	var policy struct {
		Policy string                    `json:"policy"`
		Boot   string                    `json:"guest_boot_id"`
		Proofs map[string]map[string]any `json:"proofs"`
	}
	if err := json.Unmarshal(t.PolicyReceipt, &policy); err != nil {
		return err
	}
	if policy.Policy != "pids32-kata" || policy.Boot != t.InstanceID {
		return fmt.Errorf("strict runtime policy mismatch")
	}
	for _, name := range []string{"observer_before_start", "observer_running"} {
		p := policy.Proofs[name]
		challenge, _ := p["challenge"].(string)
		if challenge == "" {
			return fmt.Errorf("guest challenge missing")
		}
		if err := kataproof.Validate(p, t.Containers["observer"], challenge, t.InstanceID); err != nil {
			return err
		}
		if p["pids_max"] != "32" || p["requested_pid_limit"] != float64(32) || p["proof_state"] != "live" {
			return fmt.Errorf("observer PID32 not proven before start and while running")
		}
	}
	return nil
}
func validateKataStop(raw json.RawMessage, t infra.ServiceTarget, service string) error {
	var receipt struct {
		Source         string              `json:"source"`
		Method         string              `json:"method"`
		Service        string              `json:"service"`
		Target         infra.ServiceTarget `json:"target"`
		Before         map[string]any      `json:"main_before"`
		Final          map[string]any      `json:"final_proof"`
		ObserverBefore map[string]any      `json:"observer_before"`
		ObserverAfter  map[string]any      `json:"observer_after"`
	}
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return err
	}
	if receipt.Source != "kata-runtime-stop-v1" || receipt.Method != "containerd.Task.Kill(SIGKILL,all)" || receipt.Service != service || receipt.Target.ExecutionID != t.ExecutionID || receipt.Target.OwnerID != t.OwnerID || receipt.Target.InstanceID != t.InstanceID || receipt.Target.Containers[service] != t.Containers[service] {
		return fmt.Errorf("stop receipt attribution mismatch")
	}
	for _, proof := range []map[string]any{receipt.Before, receipt.Final} {
		challenge, _ := proof["challenge"].(string)
		if challenge == "" {
			return fmt.Errorf("stop challenge missing")
		}
		if err := kataproof.Validate(proof, t.Containers[service], challenge, t.InstanceID); err != nil {
			return err
		}
	}
	if receipt.Final["proof_state"] != "cgroup_removed" || receipt.Final["cgroup_inode"] != receipt.Before["cgroup_inode"] || receipt.Final["cgroup_device"] != receipt.Before["cgroup_device"] || receipt.Final["cgroup_path"] != receipt.Before["cgroup_path"] {
		return fmt.Errorf("same guest cgroup removal not proven")
	}
	if service == serviceNameMain {
		before, after := receipt.ObserverBefore, receipt.ObserverAfter
		challenge, _ := after["challenge"].(string)
		if err := kataproof.Validate(after, t.Containers["observer"], challenge, t.InstanceID); err != nil {
			return err
		}
		if before["cgroup_inode"] != after["cgroup_inode"] || before["cgroup_device"] != after["cgroup_device"] || after["pids_max"] != "32" || after["populated"] != float64(1) {
			return fmt.Errorf("observer continuity not proven")
		}
	}
	return nil
}
func (i *Infra) describeKataServices(ctx context.Context, sbx infra.Sandbox, p *corev1.Pod, b config.KataServiceBinding) (infra.SandboxServices, error) {
	var state kataRuntimeState
	if err := i.kataCall(ctx, sbx, p, b, "describe", infra.ServiceAction{}, nil, &state); err != nil {
		return infra.SandboxServices{}, err
	}
	if err := validateKataPolicy(state, b.ExecutionID); err != nil {
		return infra.SandboxServices{}, err
	}
	result := infra.SandboxServices{PodUID: string(p.UID), RuntimeProfile: strictKataProfile, PolicyReceipt: state.Target.PolicyReceipt}
	for _, service := range []string{serviceNameMain, "observer"} {
		status := "running"
		if stop := state.Stops[service]; stop != nil {
			status = "unknown"
			if stop.State == "Succeeded" {
				if err := validateKataStop(stop.Receipt, state.Target, service); err != nil {
					return result, err
				}
				status = "stopped"
			}
		}
		limit := int64(0)
		if service == "observer" {
			limit = 32
		}
		result.Services = append(result.Services, infra.SandboxService{Name: service, Container: state.Target.Containers[service], State: status, StopSupported: true, PIDLimit: limit})
	}
	return result, nil
}
func (i *Infra) operateKataService(ctx context.Context, sbx infra.Sandbox, p *corev1.Pod, b config.KataServiceBinding, a infra.SandboxServiceAction) (infra.ServiceResult, error) {
	if a.Service != serviceNameMain && a.Service != "observer" {
		return infra.ServiceResult{}, serviceError(managererrors.ErrorNotFound, "service not declared")
	}
	if a.Kind == serviceOperationDownload && !((a.Service == serviceNameMain && a.Source == "/app/result.json") || (a.Service == "observer" && a.Source == "/audit/network-audit.json")) {
		return infra.ServiceResult{}, serviceError(managererrors.ErrorNotAllowed, "artifact outside frozen strict profile")
	}
	var before kataRuntimeState
	if err := i.kataCall(ctx, sbx, p, b, "describe", infra.ServiceAction{}, nil, &before); err != nil {
		return infra.ServiceResult{}, err
	}
	if err := validateKataPolicy(before, b.ExecutionID); err != nil {
		return infra.ServiceResult{}, err
	}
	var response struct {
		State  kataRuntimeState    `json:"state"`
		Result infra.ServiceResult `json:"result"`
	}
	action := infra.ServiceAction{Kind: a.Kind, Service: a.Service, Args: a.Args, Source: a.Source, TimeoutSeconds: a.TimeoutSeconds}
	if err := i.kataCall(ctx, sbx, p, b, "operate", action, &before.Target, &response); err != nil {
		return response.Result, err
	}
	if err := validateKataPolicy(response.State, b.ExecutionID); err != nil {
		return response.Result, err
	}
	if a.Kind == serviceOperationStop {
		if err := validateKataStop(response.Result.Receipt, response.State.Target, a.Service); err != nil {
			return response.Result, err
		}
	}
	return response.Result, nil
}
