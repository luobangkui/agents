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
package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/openkruise/agents/katalab/pkg/infra"
	service "github.com/openkruise/agents/pkg/sandbox-manager/infra"
)

var idPattern = regexp.MustCompile(`^ta[0-9a-z-]{8,60}$`)

type Manager struct{ backend infra.LifecycleBackend }

func New(b infra.LifecycleBackend) *Manager { return &Manager{b} }
func (m *Manager) Prepare(ctx context.Context, s infra.CreateSpec) (service.ServiceTarget, error) {
	if !idPattern.MatchString(s.ExecutionID) || s.Verifier && s.Fault != "" {
		return service.ServiceTarget{}, fmt.Errorf("invalid test execution")
	}
	switch s.Fault {
	case "", "ignore-term", "no-raw":
	default:
		return service.ServiceTarget{}, fmt.Errorf("unsupported synthetic fault")
	}
	return m.backend.Prepare(ctx, s)
}
func (m *Manager) State(ctx context.Context, id string) (json.RawMessage, error) {
	if !idPattern.MatchString(id) {
		return nil, fmt.Errorf("invalid execution id")
	}
	return m.backend.State(ctx, id)
}
func (m *Manager) Reclaim(ctx context.Context, id string) error {
	if !idPattern.MatchString(id) {
		return fmt.Errorf("invalid execution id")
	}
	return m.backend.Reclaim(ctx, id)
}
func (m *Manager) RecoverStop(ctx context.Context, t service.ServiceTarget, s string) (service.ServiceResult, error) {
	if !idPattern.MatchString(t.ExecutionID) || t.OwnerID == "" || t.InstanceID == "" || (s != "main" && s != "observer") {
		return service.ServiceResult{}, fmt.Errorf("immutable stop binding required")
	}
	return m.backend.RecoverStop(ctx, t, s)
}
