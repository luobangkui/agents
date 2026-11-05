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

// SandboxServiceBackend is an optional backend capability. Service operations
// never fall back to deleting or restarting the whole Sandbox.
type SandboxServiceBackend interface {
	DescribeServices(context.Context, Sandbox) (SandboxServices, error)
	OperateService(context.Context, Sandbox, SandboxServiceAction) (ServiceResult, error)
}
type SandboxServices struct {
	RuntimeProfile string           `json:"runtimeProfile,omitempty"`
	PolicyReceipt  json.RawMessage  `json:"policyReceipt,omitempty"`
	PodUID         string           `json:"podUID"`
	Services       []SandboxService `json:"services"`
}
type SandboxService struct {
	Name          string `json:"name"`
	Container     string `json:"container"`
	State         string `json:"state"`
	PIDLimit      int64  `json:"pidLimit,omitempty"`
	StopSupported bool   `json:"stopSupported"`
}
type SandboxServiceAction struct {
	Service        string
	Kind           string
	PodUID         string
	Args           []string
	Source         string
	TimeoutSeconds int
}
