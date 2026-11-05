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
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"time"

	agents "github.com/openkruise/agents/api/v1alpha1"
	managererrors "github.com/openkruise/agents/pkg/sandbox-manager/errors"
	"github.com/openkruise/agents/pkg/sandbox-manager/infra"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	coreclient "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/remotecommand"
	utilexec "k8s.io/client-go/util/exec"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const ServiceDeclarationAnnotation = "sandbox.agents.kruise.io/services"

const (
	serviceNameMain          = "main"
	serviceOperationStop     = "stop"
	serviceOperationDownload = "download"
)

type serviceDeclaration struct {
	Container     string   `json:"container"`
	Exec          bool     `json:"exec"`
	DownloadRoots []string `json:"downloadRoots"`
	Stop          bool     `json:"stop"`
}

func serviceError(code managererrors.ErrorCode, message string) error {
	return managererrors.NewError(code, "%s", message)
}
func (i *Infra) servicePod(ctx context.Context, sbx infra.Sandbox) (*corev1.Pod, map[string]serviceDeclaration, error) {
	box := &agents.Sandbox{}
	key := client.ObjectKey{Namespace: sbx.GetNamespace(), Name: sbx.GetName()}
	if err := i.Cache.GetClient().Get(ctx, key, box); err != nil {
		return nil, nil, err
	}
	if box.UID != sbx.GetUID() || box.DeletionTimestamp != nil || box.Annotations[agents.AnnotationOwner] != sbx.GetAnnotations()[agents.AnnotationOwner] {
		return nil, nil, serviceError(managererrors.ErrorConflict, "Sandbox identity or ownership changed")
	}
	pod := &corev1.Pod{}
	if err := i.Cache.GetClient().Get(ctx, key, pod); err != nil {
		return nil, nil, err
	}
	owner := metav1.GetControllerOf(pod)
	if owner == nil || owner.UID != box.UID || owner.Kind != "Sandbox" || pod.DeletionTimestamp != nil {
		return nil, nil, serviceError(managererrors.ErrorConflict, "Pod does not belong to this Sandbox")
	}
	if b, ok := i.kataBinding(sbx, pod); ok {
		valid := false
		for _, c := range pod.Spec.Containers {
			if c.Name == b.ControlContainer && c.SecurityContext != nil && c.SecurityContext.Privileged != nil && *c.SecurityContext.Privileged {
				for _, e := range c.Env {
					if e.Name == "SANDBOX_SERVICE_POD_UID" && e.ValueFrom != nil && e.ValueFrom.FieldRef != nil && e.ValueFrom.FieldRef.FieldPath == "metadata.uid" {
						valid = true
					}
				}
			}
		}
		if !valid || pod.Spec.HostPID || pod.Spec.HostNetwork {
			return nil, nil, serviceError(managererrors.ErrorConflict, "invalid strict supervisor isolation")
		}
		return pod, map[string]serviceDeclaration{}, nil
	}
	declarations, err := serviceDeclarations(pod)
	return pod, declarations, err
}

func serviceDeclarations(pod *corev1.Pod) (map[string]serviceDeclaration, error) {
	raw := pod.Annotations[ServiceDeclarationAnnotation]
	if raw == "" {
		return nil, serviceError(managererrors.ErrorNotAllowed, "template has not declared any accessible services")
	}
	declarations := map[string]serviceDeclaration{}
	d := json.NewDecoder(strings.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&declarations); err != nil {
		return nil, serviceError(managererrors.ErrorBadRequest, "invalid template service declaration: "+err.Error())
	}
	if d.Decode(new(any)) != io.EOF || len(declarations) == 0 || len(declarations) > 16 {
		return nil, serviceError(managererrors.ErrorBadRequest, "invalid template service declaration")
	}
	containers := map[string]bool{}
	identities := map[string]bool{}
	for _, c := range pod.Spec.Containers {
		containers[c.Name] = true
		for _, env := range c.Env {
			if env.Name == "SANDBOX_SERVICE_POD_UID" && env.ValueFrom != nil && env.ValueFrom.FieldRef != nil && env.ValueFrom.FieldRef.FieldPath == "metadata.uid" {
				identities[c.Name] = true
			}
		}
	}
	for name, entry := range declarations {
		if len(validation.IsDNS1123Label(name)) != 0 || !containers[entry.Container] || !identities[entry.Container] {
			return nil, serviceError(managererrors.ErrorBadRequest, "service declaration requires a regular container with downward API SANDBOX_SERVICE_POD_UID")
		}
		for _, root := range entry.DownloadRoots {
			if root == "/" || !path.IsAbs(root) || path.Clean(root) != root || strings.ContainsRune(root, 0) {
				return nil, serviceError(managererrors.ErrorBadRequest, "invalid download root in template")
			}
		}
	}
	return declarations, nil
}
func serviceStatus(p *corev1.Pod, container string) (corev1.ContainerStatus, error) {
	for _, s := range p.Status.ContainerStatuses {
		if s.Name == container {
			if s.RestartCount != 0 {
				return s, serviceError(managererrors.ErrorConflict, "service restarted; original incarnation unavailable")
			}
			return s, nil
		}
	}
	return corev1.ContainerStatus{}, serviceError(managererrors.ErrorConflict, "service has no observed container")
}
func singleStopSupported(p *corev1.Pod) bool {
	return p.Spec.RestartPolicy == corev1.RestartPolicyNever && (p.Spec.ShareProcessNamespace == nil || !*p.Spec.ShareProcessNamespace) && !p.Spec.HostPID
}
func (i *Infra) DescribeServices(ctx context.Context, sbx infra.Sandbox) (infra.SandboxServices, error) {
	p, entries, err := i.servicePod(ctx, sbx)
	if err != nil {
		return infra.SandboxServices{}, err
	}
	if b, ok := i.kataBinding(sbx, p); ok {
		return i.describeKataServices(ctx, sbx, p, b)
	}
	out := infra.SandboxServices{PodUID: string(p.UID), Services: []infra.SandboxService{}}
	names := []string{}
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		e := entries[name]
		state := "pending"
		s, err := serviceStatus(p, e.Container)
		if err != nil {
			return out, err
		}
		if s.State.Running != nil {
			state = "running"
		} else if s.State.Terminated != nil {
			state = "stopped"
		}
		out.Services = append(out.Services, infra.SandboxService{Name: name, Container: e.Container, State: state, StopSupported: e.Stop && singleStopSupported(p)})
	}
	return out, nil
}

// commandSupervisor kills the command process group on timeout, output overflow,
// transport stdin EOF or termination. It runs with the container's configured UID.
const commandSupervisor = `import os,sys,subprocess,selectors,signal,time,json
assert os.environ.get('SANDBOX_SERVICE_POD_UID')==sys.argv[3],'Pod UID mismatch'
args=json.loads(sys.argv[1]);limit=1024*1024;deadline=time.monotonic()+float(sys.argv[2]);child=None
try:
 child=subprocess.Popen(args,start_new_session=True,stdout=subprocess.PIPE,stderr=subprocess.PIPE)
 def terminate(*_):
  if child.poll() is None:os.killpg(child.pid,signal.SIGKILL)
  raise SystemExit(124)
 signal.signal(signal.SIGTERM,terminate);signal.signal(signal.SIGINT,terminate)
 sel=selectors.DefaultSelector();sel.register(child.stdout,selectors.EVENT_READ,sys.stdout.buffer);sel.register(child.stderr,selectors.EVENT_READ,sys.stderr.buffer);sel.register(sys.stdin.buffer,selectors.EVENT_READ,None);sizes={sys.stdout.buffer:0,sys.stderr.buffer:0}
 while True:
  if time.monotonic()>=deadline:terminate()
  for key,_ in sel.select(min(0.1,max(0,deadline-time.monotonic()))):
   data=os.read(key.fd,65536)
   if key.data is None:
    if not data:terminate()
   elif data:
    sizes[key.data]+=len(data)
    if sizes[key.data]>limit:terminate()
    key.data.write(data);key.data.flush()
   else:sel.unregister(key.fileobj)
  if child.poll() is not None and len(sel.get_map())<=1:break
 code=child.returncode
 raise SystemExit(code if code>=0 else 128-code)
finally:
 if child is not None:
  try:os.killpg(child.pid,signal.SIGKILL)
  except ProcessLookupError:pass
  child.wait()
`
const readServiceFile = `import os,sys,stat,json
assert os.environ.get('SANDBOX_SERVICE_POD_UID')==sys.argv[3],'Pod UID mismatch'
p=sys.argv[1];roots=json.loads(sys.argv[2]);fd=os.open(p,os.O_RDONLY|os.O_NONBLOCK|os.O_NOFOLLOW)
try:
 s=os.fstat(fd);actual=os.path.realpath('/proc/self/fd/'+str(fd));allowed=[os.path.realpath(r) for r in roots]
 assert any(os.path.commonpath([actual,r])==r for r in allowed),'file outside declared roots'
 assert stat.S_ISREG(s.st_mode),'regular file required'
 assert s.st_size<=8*1024*1024,'artifact too large'
 chunks=[];size=0
 while True:
  chunk=os.read(fd,65536)
  if not chunk:break
  size+=len(chunk);assert size<=8*1024*1024,'artifact too large';chunks.append(chunk)
 sys.stdout.buffer.write(b''.join(chunks))
finally:os.close(fd)
`

type serviceBuffer struct {
	bytes.Buffer
	limit int
}

func (b *serviceBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		return 0, fmt.Errorf("service output exceeds limit")
	}
	return b.Buffer.Write(p)
}
func (i *Infra) serviceExec(ctx context.Context, p *corev1.Pod, container string, args []string, keepalive bool, limit int) (infra.ServiceResult, error) {
	var out infra.ServiceResult
	if i.serviceConfig == nil {
		return out, serviceError(managererrors.ErrorBadRequest, "service execution transport is not configured")
	}
	core, err := coreclient.NewForConfig(i.serviceConfig)
	if err != nil {
		return out, err
	}
	req := core.RESTClient().Post().Namespace(p.Namespace).Resource("pods").Name(p.Name).SubResource("exec").VersionedParams(&corev1.PodExecOptions{Container: container, Command: args, Stdout: true, Stderr: true, Stdin: keepalive}, clientgoscheme.ParameterCodec)
	executor, err := remotecommand.NewSPDYExecutor(i.serviceConfig, "POST", req.URL())
	if err != nil {
		return out, err
	}
	stdout := &serviceBuffer{limit: limit}
	stderr := &serviceBuffer{limit: 1024 * 1024}
	opts := remotecommand.StreamOptions{Stdout: stdout, Stderr: stderr}
	if keepalive {
		reader, writer := io.Pipe()
		defer reader.Close()
		defer writer.Close()
		opts.Stdin = reader
		stop := make(chan struct{})
		defer close(stop)
		go func() {
			tick := time.NewTicker(500 * time.Millisecond)
			defer tick.Stop()
			for {
				select {
				case <-ctx.Done():
					writer.Close()
					return
				case <-stop:
					return
				case <-tick.C:
					if _, err := writer.Write([]byte(".\n")); err != nil {
						return
					}
				}
			}
		}()
	}
	err = executor.StreamWithContext(ctx, opts)
	out.Stdout = stdout.Bytes()
	out.Stderr = stderr.Bytes()
	var exit utilexec.ExitError
	if errors.As(err, &exit) {
		out.ExitCode = exit.ExitStatus()
		err = nil
	}
	return out, err
}
func (i *Infra) OperateService(ctx context.Context, sbx infra.Sandbox, a infra.SandboxServiceAction) (infra.ServiceResult, error) {
	var out infra.ServiceResult
	select {
	case i.serviceSlots <- struct{}{}:
		defer func() { <-i.serviceSlots }()
	case <-ctx.Done():
		return out, ctx.Err()
	}
	p, entries, err := i.servicePod(ctx, sbx)
	if err != nil {
		return out, err
	}
	if string(p.UID) != a.PodUID {
		return out, serviceError(managererrors.ErrorConflict, "Pod UID changed")
	}
	if b, ok := i.kataBinding(sbx, p); ok {
		return i.operateKataService(ctx, sbx, p, b, a)
	}
	e, ok := entries[a.Service]
	if !ok {
		return out, serviceError(managererrors.ErrorNotFound, "service not declared")
	}
	original, err := serviceStatus(p, e.Container)
	if err != nil {
		return out, err
	}
	if a.Kind != serviceOperationStop && original.State.Running == nil {
		return out, serviceError(managererrors.ErrorConflict, "service is not running")
	}
	switch a.Kind {
	case "exec":
		if !e.Exec {
			return out, serviceError(managererrors.ErrorNotAllowed, "exec is not permitted for this service")
		}
		raw, _ := json.Marshal(a.Args)
		out, err = i.serviceExec(ctx, p, e.Container, []string{"python", "-I", "-S", "-c", commandSupervisor, string(raw), fmt.Sprint(a.TimeoutSeconds), a.PodUID}, true, 1024*1024)
	case serviceOperationDownload:
		allowed := false
		for _, root := range e.DownloadRoots {
			if a.Source == root || strings.HasPrefix(a.Source, root+"/") {
				allowed = true
			}
		}
		if !allowed {
			return out, serviceError(managererrors.ErrorNotAllowed, "path outside declared download roots")
		}
		roots, _ := json.Marshal(e.DownloadRoots)
		out, err = i.serviceExec(ctx, p, e.Container, []string{"python", "-I", "-S", "-c", readServiceFile, a.Source, string(roots), a.PodUID}, false, 8*1024*1024)
		if err == nil && out.ExitCode != 0 {
			err = serviceError(managererrors.ErrorBadRequest, "file download failed: "+string(out.Stderr))
		}
	case serviceOperationStop:
		return i.stopNativeService(ctx, sbx, p, e, original, a)
	default:
		return out, serviceError(managererrors.ErrorBadRequest, "unsupported service operation")
	}
	if err != nil {
		return out, err
	}
	latest, _, err := i.servicePod(ctx, sbx)
	if err != nil {
		return out, err
	}
	if latest.UID != p.UID {
		return out, serviceError(managererrors.ErrorConflict, "Pod replaced during operation")
	}
	current, err := serviceStatus(latest, e.Container)
	if err != nil {
		return out, err
	}
	if current.ContainerID != original.ContainerID {
		return out, serviceError(managererrors.ErrorConflict, "container replaced during operation")
	}
	return out, nil
}

func (i *Infra) stopNativeService(ctx context.Context, sbx infra.Sandbox, p *corev1.Pod, e serviceDeclaration, original corev1.ContainerStatus, a infra.SandboxServiceAction) (infra.ServiceResult, error) {
	var out infra.ServiceResult
	if !e.Stop || !singleStopSupported(p) {
		return out, serviceError(managererrors.ErrorConflict, "single-service stop requires declared permission, Never restart and isolated PID namespace")
	}
	if original.State.Terminated == nil {
		_, signalErr := i.serviceExec(ctx, p, e.Container, []string{"python", "-I", "-S", "-c", "import os,sys,signal;assert os.environ.get('SANDBOX_SERVICE_POD_UID')==sys.argv[1],'Pod UID mismatch';os.kill(1,signal.SIGTERM)", a.PodUID}, false, 1024)
		if signalErr != nil && ctx.Err() != nil {
			return out, fmt.Errorf("stop outcome unknown: %w", signalErr)
		}
	}
	for {
		latest, _, readErr := i.servicePod(ctx, sbx)
		if readErr != nil {
			return out, readErr
		}
		if latest.UID != p.UID {
			return out, serviceError(managererrors.ErrorConflict, "Pod replaced during stop")
		}
		current, readErr := serviceStatus(latest, e.Container)
		if readErr != nil {
			return out, readErr
		}
		if current.ContainerID != original.ContainerID {
			return out, serviceError(managererrors.ErrorConflict, "service replaced during stop")
		}
		if current.State.Terminated != nil {
			out.Receipt, _ = json.Marshal(map[string]any{"service": a.Service, "podUID": a.PodUID, "containerID": current.ContainerID, "exitCode": current.State.Terminated.ExitCode, "restartCount": current.RestartCount, "finishedAt": current.State.Terminated.FinishedAt, "method": "KubernetesContainerTerminationObservation", "targetProcessesEmpty": nil})
			return out, nil
		}
		select {
		case <-ctx.Done():
			return out, fmt.Errorf("stop outcome unknown; do not blindly retry: %w", ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}
