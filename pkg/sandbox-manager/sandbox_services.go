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
package sandbox_manager

import (
	"context"
	"path"
	"strings"
	"time"

	"github.com/openkruise/agents/api/v1alpha1"
	managererrors "github.com/openkruise/agents/pkg/sandbox-manager/errors"
	"github.com/openkruise/agents/pkg/sandbox-manager/infra"
)

func (m *SandboxManager) DescribeSandboxServices(ctx context.Context, user string, sbx infra.Sandbox) (infra.SandboxServices, error) {
	if user == "" || sbx.GetAnnotations()[v1alpha1.AnnotationOwner] != user {
		return infra.SandboxServices{}, managererrors.NewError(managererrors.ErrorNotAllowed, "sandbox is not owned")
	}
	backend, ok := m.infra.(infra.SandboxServiceBackend)
	if !ok {
		return infra.SandboxServices{}, managererrors.NewError(managererrors.ErrorBadRequest, "sandbox backend does not support service operations")
	}
	return backend.DescribeServices(ctx, sbx)
}
func (m *SandboxManager) OperateSandboxService(ctx context.Context, user string, sbx infra.Sandbox, a infra.SandboxServiceAction) (infra.ServiceResult, error) {
	if user == "" || sbx.GetAnnotations()[v1alpha1.AnnotationOwner] != user {
		return infra.ServiceResult{}, managererrors.NewError(managererrors.ErrorNotAllowed, "sandbox is not owned")
	}
	bad := func(message string) (infra.ServiceResult, error) {
		return infra.ServiceResult{}, managererrors.NewError(managererrors.ErrorBadRequest, "%s", message)
	}
	if a.PodUID == "" {
		return bad("podUID is required; obtain it from the services endpoint")
	}
	if a.Service == "" || strings.ContainsAny(a.Service, "/\\") || strings.ContainsRune(a.Service, 0) {
		return bad("invalid service")
	}
	if a.TimeoutSeconds < 1 || a.TimeoutSeconds > 60 {
		return bad("timeoutSeconds must be in 1..60")
	}
	switch a.Kind {
	case "exec":
		if len(a.Args) == 0 || len(a.Args) > 128 {
			return bad("args must contain 1..128 entries")
		}
		n := 0
		for _, s := range a.Args {
			n += len(s)
			if strings.ContainsRune(s, 0) {
				return bad("args cannot contain NUL")
			}
		}
		if n > 60*1024 {
			return bad("args exceed 60 KiB")
		}
	case "download":
		if !path.IsAbs(a.Source) || path.Clean(a.Source) != a.Source || strings.ContainsRune(a.Source, 0) {
			return bad("path must be absolute and clean")
		}
	case "stop":
	default:
		return bad("unsupported service operation")
	}
	backend, ok := m.infra.(infra.SandboxServiceBackend)
	if !ok {
		return bad("sandbox backend does not support service operations")
	}
	ctx, done := context.WithTimeout(ctx, time.Duration(a.TimeoutSeconds)*time.Second)
	defer done()
	return backend.OperateService(ctx, sbx, a)
}
