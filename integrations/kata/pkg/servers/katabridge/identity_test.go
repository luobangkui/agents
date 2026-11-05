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
package katabridge

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestIdentityRejectsUntrustedConfiguration(t *testing.T) {
	for _, scenario := range []string{"pod-mismatch", "missing-field", "unknown-field", "two-objects"} {
		t.Run(scenario, func(t *testing.T) {
			t.Setenv("POD_UID", "actual-pod")
			v := map[string]string{"provider_id": "provider-123", "cluster_uid": "cluster-123", "node_uid": "node-123", "node_boot_id": "boot-123", "pod_uid": "different-pod", "runtime_epoch": "epoch-123"}
			switch scenario {
			case "missing-field":
				delete(v, "runtime_epoch")
			case "unknown-field":
				v["extra"] = "untrusted"
			}
			raw, err := json.Marshal(v)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "two-objects" {
				raw = append(raw, []byte(`{}`)...)
			}
			if _, err = WithIdentity(http.NotFoundHandler(), raw, "01234567890123456789012345678901"); err == nil {
				t.Fatal("untrusted deployment identity accepted")
			}
		})
	}
}
