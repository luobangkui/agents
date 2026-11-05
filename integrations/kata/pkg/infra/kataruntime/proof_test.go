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
package kataruntime

import (
	"encoding/json"
	"testing"
)

func validProof() map[string]any {
	p := map[string]any{}
	_ = json.Unmarshal([]byte(`{"proof_state":"live","schema_version":1,"source":"kata-agent-runtime-cgroup","container_id":"main","challenge":"nonce","guest_boot_id":"boot","cgroup_path":"/sys/fs/cgroup/taskarena/main","cgroup_inode":42,"cgroup_device":29,"pids_current":0,"populated":0,"processes":[],"pids_max":"max","observed_unix_ns":"1793785578458017000"}`), &p)
	return p
}
func TestRejectInvalidRuntimeProof(t *testing.T) {
	cases := []struct {
		name, key string
		value     any
	}{
		{"wrong container", "container_id", "other"}, {"replay", "challenge", "old"}, {"guest replaced", "guest_boot_id", "other"}, {"self report", "source", "workload"}, {"missing inode", "cgroup_inode", nil}, {"root cgroup", "cgroup_path", "/sys/fs/cgroup"}, {"path traversal", "cgroup_path", "/sys/fs/cgroup/a/../b"}, {"negative count", "pids_current", float64(-1)}, {"missing processes", "processes", nil}, {"inconsistent processes", "processes", []any{float64(2)}}, {"invalid populated", "populated", float64(2)}, {"missing limit", "pids_max", nil}, {"invalid limit", "pids_max", "-1"}, {"invalid timestamp", "observed_unix_ns", "unknown"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := validProof()
			p[c.key] = c.value
			if validateProof(p, "main", "nonce", "boot") == nil {
				t.Fatal("accepted invalid evidence")
			}
		})
	}
	if e := validateProof(validProof(), "main", "nonce", "boot"); e != nil {
		t.Fatal(e)
	}
}

func TestRemovalProofRequiresKernelReadingsAndConfirmedRemoval(t *testing.T) {
	p := validProof()
	p["proof_state"] = "cgroup_removed"
	if validateProof(p, "main", "nonce", "boot") == nil {
		t.Fatal("accepted missing removal evidence")
	}
	p["empty_samples"] = float64(2)
	p["removal_confirmed"] = true
	p["prior_empty_observed_unix_ns"] = "1793785578458016000"
	p["removal_unix_ns"] = "1793785578458018000"
	p["response_unix_ns"] = "1793785578458019000"
	if e := validateProof(p, "main", "nonce", "boot"); e != nil {
		t.Fatal(e)
	}
	p["pids_current"] = float64(1)
	if validateProof(p, "main", "nonce", "boot") == nil {
		t.Fatal("accepted residual task")
	}
	p["pids_current"] = float64(0)
	p["response_unix_ns"] = "1"
	if validateProof(p, "main", "nonce", "boot") == nil {
		t.Fatal("accepted reversed chronology")
	}
}
