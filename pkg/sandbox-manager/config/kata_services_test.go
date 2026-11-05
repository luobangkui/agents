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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStrictKataBindingsAreExplicitAndImmutable(t *testing.T) {
	entries, err := LoadKataServiceBindings("")
	require.NoError(t, err)
	require.Nil(t, entries)
	for _, scenario := range []string{"valid", "wrong-namespace", "wrong-pod", "cross-execution", "bad-container", "missing-token", "duplicate", "extra-object"} {
		t.Run(scenario, func(t *testing.T) {
			b := KataServiceBinding{Namespace: "ta-kata-strict-demo", SandboxUID: "11111111-1111-4111-8111-111111111111", PodUID: "22222222-2222-4222-8222-222222222222", ControlContainer: "executor", ControlToken: strings.Repeat("test", 8), ExecutionID: "ta-11111111111141118111111111111111"}
			switch scenario {
			case "wrong-namespace":
				b.Namespace = "production"
			case "wrong-pod":
				b.PodUID = "not-a-uuid"
			case "cross-execution":
				b.ExecutionID = "ta-foreign"
			case "bad-container":
				b.ControlContainer = "sandbox"
			case "missing-token":
				b.ControlToken = ""
			}
			items := []KataServiceBinding{b}
			if scenario == "duplicate" {
				items = append(items, b)
			}
			raw, err := json.Marshal(items)
			require.NoError(t, err)
			if scenario == "extra-object" {
				raw = append(raw, []byte(`{}`)...)
			}
			p := filepath.Join(t.TempDir(), "bindings.json")
			require.NoError(t, os.WriteFile(p, raw, 0600))
			_, err = LoadKataServiceBindings(p)
			if scenario == "valid" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}
