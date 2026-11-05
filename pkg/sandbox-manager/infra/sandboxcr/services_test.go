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
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	agents "github.com/openkruise/agents/api/v1alpha1"
	"github.com/openkruise/agents/pkg/sandbox-manager/infra"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestDeclaredSandboxServiceGuards(t *testing.T) {
	for _, scenario := range []string{"valid", "undeclared", "wrong-owner", "wrong-pod", "undeclared-service", "restarted", "shared-pid", "always-restart", "root-path-declaration", "init-container", "bad-container", "exec-disabled", "path-denied"} {
		t.Run(scenario, func(t *testing.T) {
			i, c := NewTestInfra(t)
			box := createTestSandbox("service-test", "owner", agents.SandboxRunning, true)
			box.UID = types.UID("box-uid")
			require.NoError(t, c.Create(t.Context(), box))
			boxAs := AsSandbox(box, i.Cache)
			declaration := `{"main":{"container":"sandbox","exec":true,"downloadRoots":["/app"],"stop":true}}`
			yes := true
			p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: box.Name, Namespace: box.Namespace, UID: types.UID("pod-uid"), Annotations: map[string]string{ServiceDeclarationAnnotation: declaration}, OwnerReferences: []metav1.OwnerReference{{Kind: "Sandbox", UID: box.UID, Controller: &yes}}}, Spec: corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever, Containers: []corev1.Container{{Name: "sandbox", Env: []corev1.EnvVar{{Name: "SANDBOX_SERVICE_POD_UID", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.uid"}}}}}}}, Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{Name: "sandbox", ContainerID: "containerd://one", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}}}
			switch scenario {
			case "undeclared":
				p.Annotations = nil
			case "wrong-owner":
				p.OwnerReferences[0].UID = "other"
			case "restarted":
				p.Status.ContainerStatuses[0].RestartCount = 1
			case "shared-pid":
				p.Spec.ShareProcessNamespace = &yes
			case "always-restart":
				p.Spec.RestartPolicy = corev1.RestartPolicyAlways
			case "root-path-declaration":
				p.Annotations[ServiceDeclarationAnnotation] = strings.Replace(declaration, "/app", "/", 1)
			case "init-container":
				p.Spec.Containers = nil
				p.Spec.InitContainers = []corev1.Container{{Name: "sandbox"}}
			case "bad-container":
				p.Annotations[ServiceDeclarationAnnotation] = strings.Replace(declaration, "sandbox", "other", 1)
			case "exec-disabled":
				p.Annotations[ServiceDeclarationAnnotation] = strings.Replace(declaration, `"exec":true`, `"exec":false`, 1)
			}
			require.NoError(t, c.Create(t.Context(), p))
			require.NoError(t, c.Status().Update(t.Context(), p))
			var observed corev1.Pod
			require.NoError(t, i.Cache.GetClient().Get(t.Context(), client.ObjectKeyFromObject(p), &observed))
			a := infra.SandboxServiceAction{Service: "main", Kind: "exec", PodUID: "pod-uid", Args: []string{"true"}, TimeoutSeconds: 1}
			switch scenario {
			case "wrong-pod":
				a.PodUID = "other"
			case "undeclared-service":
				a.Service = "observer"
			case "shared-pid", "always-restart":
				a.Kind = "stop"
			case "path-denied":
				a.Kind = "download"
				a.Source = "/etc/passwd"
			}
			if scenario == "valid" {
				result, err := i.DescribeServices(t.Context(), boxAs)
				require.NoError(t, err)
				require.Equal(t, "pod-uid", result.PodUID)
				require.True(t, result.Services[0].StopSupported)
				return
			}
			ctx, done := context.WithTimeout(t.Context(), time.Second)
			defer done()
			_, err := i.OperateService(ctx, boxAs, a)
			require.Error(t, err)
		})
	}
}
func TestCommandSupervisorTimeoutAndBoundedOutput(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	for _, scenario := range []string{"exit", "timeout", "overflow", "disconnect"} {
		t.Run(scenario, func(t *testing.T) {
			code := "print('ok')"
			deadline := "3"
			if scenario == "timeout" {
				code = "import time;time.sleep(10)"
				deadline = "0.2"
			}
			if scenario == "overflow" {
				code = "print('x'*2000000)"
			}
			if scenario == "disconnect" {
				code = "import time;time.sleep(10)"
			}
			args, _ := json.Marshal([]string{python, "-c", code})
			cmd := exec.Command(python, "-c", commandSupervisor, string(args), deadline, "test-pod")
			cmd.Env = append(os.Environ(), "SANDBOX_SERVICE_POD_UID=test-pod")
			reader, writer, err := os.Pipe()
			require.NoError(t, err)
			defer reader.Close()
			defer writer.Close()
			cmd.Stdin = reader
			cmd.Stdout = io.Discard
			cmd.Stderr = io.Discard
			require.NoError(t, cmd.Start())
			if scenario == "disconnect" {
				writer.Close()
			}
			err = cmd.Wait()
			if scenario == "exit" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}
