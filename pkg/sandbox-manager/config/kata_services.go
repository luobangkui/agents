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
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/google/uuid"
	"k8s.io/apimachinery/pkg/util/validation"
)

// KataServiceBinding authorizes one immutable supervisor incarnation. Credentials
// remain in that supervisor; clients cannot supply an endpoint or runtime target.
type KataServiceBinding struct {
	Namespace        string `json:"namespace"`
	SandboxUID       string `json:"sandboxUID"`
	PodUID           string `json:"podUID"`
	ControlToken     string `json:"controlToken"`
	ControlContainer string `json:"controlContainer"`
	ExecutionID      string `json:"executionID"`
}

func LoadKataServiceBindings(path string) ([]KataServiceBinding, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var entries []KataServiceBinding
	d := json.NewDecoder(f)
	d.DisallowUnknownFields()
	if err = d.Decode(&entries); err != nil {
		return nil, err
	}
	if d.Decode(new(any)) != io.EOF {
		return nil, fmt.Errorf("one bindings array required")
	}
	if len(entries) == 0 || len(entries) > 16 {
		return nil, fmt.Errorf("1..16 strict Kata bindings required")
	}
	seen := map[string]bool{}
	for _, b := range entries {
		if _, err := uuid.Parse(b.SandboxUID); err != nil {
			return nil, fmt.Errorf("invalid Sandbox UID")
		}
		if _, err := uuid.Parse(b.PodUID); err != nil {
			return nil, fmt.Errorf("invalid Pod UID")
		}
		if len(validation.IsDNS1123Label(b.Namespace)) != 0 {
			return nil, fmt.Errorf("invalid namespace")
		}
		key := b.Namespace + "/" + b.SandboxUID
		if !strings.HasPrefix(b.Namespace, "ta-kata-") || b.SandboxUID == "" || b.PodUID == "" || len(b.ControlToken) < 32 || b.ControlContainer != "executor" || b.ExecutionID != "ta-"+strings.ReplaceAll(b.SandboxUID, "-", "") || seen[key] {
			return nil, fmt.Errorf("invalid or duplicate immutable Kata binding")
		}
		seen[key] = true
	}
	return entries, nil
}
