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
package infra

import (
	"context"
	"encoding/json"

	service "github.com/openkruise/agents/pkg/sandbox-manager/infra"
)

type CreateSpec struct {
	ExecutionID string `json:"execution_id"`
	Verifier    bool   `json:"verifier"`
	Fault       string `json:"fault,omitempty"`
}
type LifecycleBackend interface {
	Prepare(context.Context, CreateSpec) (service.ServiceTarget, error)
	State(context.Context, string) (json.RawMessage, error)
	Reclaim(context.Context, string) error
	RecoverStop(context.Context, service.ServiceTarget, string) (service.ServiceResult, error)
}
