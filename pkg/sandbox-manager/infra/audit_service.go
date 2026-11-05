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
)

// ServiceTarget pins one execution and both service incarnations. These opaque
// identifiers are resolved by the trusted test control plane, never by an agent.
type ServiceTarget struct {
	PolicyReceipt json.RawMessage   `json:"policy_receipt,omitempty"`
	ExecutionID   string            `json:"execution_id"`
	OwnerID       string            `json:"owner_id"`
	InstanceID    string            `json:"instance_id"`
	Containers    map[string]string `json:"containers"`
}
type ServiceAction struct {
	User           string   `json:"user,omitempty"`
	Kind           string   `json:"kind"`
	Service        string   `json:"service"`
	Args           []string `json:"args,omitempty"`
	Source         string   `json:"source,omitempty"`
	TimeoutSeconds int      `json:"timeout_seconds"`
}
type ServiceResult struct {
	Stdout   []byte          `json:"stdout,omitempty"`
	Stderr   []byte          `json:"stderr,omitempty"`
	ExitCode int             `json:"exit_code"`
	Receipt  json.RawMessage `json:"receipt,omitempty"`
}
type AuditServiceBackend interface {
	ExecuteService(context.Context, ServiceTarget, ServiceAction) (ServiceResult, error)
}
