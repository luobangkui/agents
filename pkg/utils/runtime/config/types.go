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

package config

import (
	"time"

	"github.com/google/uuid"
)

type InitRuntimeOptions struct {
	EnvVars     map[string]string `json:"envVars,omitempty"`
	AccessToken string            `json:"accessToken,omitempty"`
	ReInit      bool              `json:"-"`
	SkipRefresh bool              `json:"skipRefresh,omitempty"`
}

// NewDefaultAccessToken generates a default access token using UUID.
func NewDefaultAccessToken() string {
	return uuid.NewString()
}

const (
	DefaultCSIMountConcurrency = 3
	DefaultCSIMountTimeout     = 30 * time.Second
)

type CSIMountOptions struct {
	MountOptionList    []MountConfig `json:"mountOptionList"`
	MountOptionListRaw string        `json:"mountOptionListRaw"` // the raw JSON string for mount options
	// Concurrency limits concurrent CSI mounts. Non-positive values use DefaultCSIMountConcurrency.
	Concurrency int `json:"concurrency,omitempty"`
	// Timeout limits one CSI mount. Non-positive values use DefaultCSIMountTimeout.
	Timeout time.Duration `json:"timeout,omitempty"`
}

type MountConfig struct {
	Driver     string `json:"driver"`
	RequestRaw string `json:"requestRaw"`
}
