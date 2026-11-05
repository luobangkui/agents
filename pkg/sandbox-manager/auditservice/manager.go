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
// Package auditservice validates service use cases independently of transport.
package auditservice

import (
	"context"
	"time"

	domainerrors "github.com/openkruise/agents/pkg/sandbox-manager/errors"
	"github.com/openkruise/agents/pkg/sandbox-manager/infra"
)

type Manager struct{ backend infra.AuditServiceBackend }

func New(backend infra.AuditServiceBackend) *Manager { return &Manager{backend: backend} }
func (m *Manager) Execute(ctx context.Context, target infra.ServiceTarget, action infra.ServiceAction) (infra.ServiceResult, error) {
	if target.ExecutionID == "" || target.OwnerID == "" || target.InstanceID == "" {
		return infra.ServiceResult{}, domainerrors.NewError(domainerrors.ErrorBadRequest, "immutable execution binding required")
	}
	if action.Service != "main" && action.Service != "observer" {
		return infra.ServiceResult{}, domainerrors.NewError(domainerrors.ErrorBadRequest, "unknown service")
	}
	if action.TimeoutSeconds < 1 || action.TimeoutSeconds > 60 {
		return infra.ServiceResult{}, domainerrors.NewError(domainerrors.ErrorBadRequest, "deadline must be within 1..60 seconds")
	}
	switch action.Kind {
	case "exec", "download", "stop":
	case "collect":
		if action.Service != "observer" {
			return infra.ServiceResult{}, domainerrors.NewError(domainerrors.ErrorBadRequest, "collect requires observer")
		}
	default:
		return infra.ServiceResult{}, domainerrors.NewError(domainerrors.ErrorBadRequest, "unknown operation")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(action.TimeoutSeconds)*time.Second)
	defer cancel()
	return m.backend.ExecuteService(ctx, target, action)
}
