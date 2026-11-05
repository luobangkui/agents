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
package sandboxcr

import (
	"encoding/json"
	"testing"

	"github.com/openkruise/agents/pkg/sandbox-manager/infra"
	"github.com/stretchr/testify/require"
)

func strictProof(container, limit string, inode int) map[string]any {
	return map[string]any{"schema_version": 1, "source": "kata-agent-runtime-cgroup", "container_id": container, "challenge": "challenge", "guest_boot_id": "guest-boot", "cgroup_path": "/sys/fs/cgroup/taskarena/" + container, "cgroup_inode": inode, "cgroup_device": 29, "pids_current": 1, "populated": 1, "processes": []int{1}, "pids_max": limit, "observed_unix_ns": "10", "proof_state": "live", "requested_pid_limit": 32}
}
func strictState(t *testing.T) kataRuntimeState {
	t.Helper()
	id := "ta-execution"
	p := strictProof(id+"-observer", "32", 42)
	raw, err := json.Marshal(map[string]any{"policy": "pids32-kata", "guest_boot_id": "guest-boot", "proofs": map[string]any{"observer_before_start": p, "observer_running": p}})
	require.NoError(t, err)
	return kataRuntimeState{Phase: "Running", Target: infra.ServiceTarget{ExecutionID: id, OwnerID: "owner", InstanceID: "guest-boot", Containers: map[string]string{"main": id + "-main", "observer": id + "-observer"}, PolicyReceipt: raw}}
}
func TestStrictKataPolicyRejectsMissingPIDProof(t *testing.T) {
	for _, scenario := range []string{"valid", "reclaimed", "wrong-execution", "guest-replaced", "missing-proof", "unlimited", "late-only", "wrong-container"} {
		t.Run(scenario, func(t *testing.T) {
			s := strictState(t)
			var p map[string]any
			require.NoError(t, json.Unmarshal(s.Target.PolicyReceipt, &p))
			proofs := p["proofs"].(map[string]any)
			before := proofs["observer_before_start"].(map[string]any)
			switch scenario {
			case "reclaimed":
				s.Reclaimed = true
			case "wrong-execution":
				s.Target.ExecutionID = "ta-other"
			case "guest-replaced":
				s.Target.InstanceID = "other"
			case "missing-proof":
				delete(proofs, "observer_running")
			case "unlimited":
				before["pids_max"] = "max"
			case "late-only":
				delete(proofs, "observer_before_start")
			case "wrong-container":
				s.Target.Containers["observer"] = "foreign"
			}
			raw, err := json.Marshal(p)
			require.NoError(t, err)
			s.Target.PolicyReceipt = raw
			err = validateKataPolicy(s, "ta-execution")
			if scenario == "valid" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}
func TestStrictKataStopRejectsIncompleteGuestRemoval(t *testing.T) {
	for _, scenario := range []string{"valid", "self-report", "wrong-service", "wrong-guest", "process-remains", "not-removed", "one-empty-sample", "replaced-cgroup", "observer-replaced", "observer-unlimited"} {
		t.Run(scenario, func(t *testing.T) {
			s := strictState(t)
			before := strictProof(s.Target.Containers["main"], "max", 41)
			after := strictProof(s.Target.Containers["main"], "max", 41)
			after["proof_state"] = "cgroup_removed"
			after["pids_current"] = 0
			after["populated"] = 0
			after["processes"] = []int{}
			after["empty_samples"] = 2
			after["removal_confirmed"] = true
			after["prior_empty_observed_unix_ns"] = "9"
			after["removal_unix_ns"] = "11"
			after["response_unix_ns"] = "12"
			ob := strictProof(s.Target.Containers["observer"], "32", 42)
			oa := strictProof(s.Target.Containers["observer"], "32", 42)
			r := map[string]any{"source": "kata-runtime-stop-v1", "method": "containerd.Task.Kill(SIGKILL,all)", "service": "main", "target": s.Target, "main_before": before, "final_proof": after, "observer_before": ob, "observer_after": oa}
			switch scenario {
			case "self-report":
				r["source"] = "workload"
			case "wrong-service":
				r["service"] = "observer"
			case "wrong-guest":
				after["guest_boot_id"] = "other"
			case "process-remains":
				after["pids_current"] = 1
				after["processes"] = []int{999}
			case "not-removed":
				after["removal_confirmed"] = false
			case "one-empty-sample":
				after["empty_samples"] = 1
			case "replaced-cgroup":
				after["cgroup_inode"] = 99
			case "observer-replaced":
				oa["cgroup_inode"] = 99
			case "observer-unlimited":
				oa["pids_max"] = "max"
			}
			raw, err := json.Marshal(r)
			require.NoError(t, err)
			err = validateKataStop(raw, s.Target, "main")
			if scenario == "valid" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}
