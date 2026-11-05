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
package kataproof

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// Validate validates data obtained over the trusted host-to-agent channel.
// It does not turn JSON submitted by a workload into runtime evidence.
func Validate(p map[string]any, target, challenge, boot string) error {
	if err := validateAttribution(p, target, challenge, boot); err != nil {
		return err
	}
	current, ok := p["pids_current"].(float64)
	if !ok || current < 0 || current != float64(uint64(current)) {
		return fmt.Errorf("invalid task count")
	}
	populated, ok := p["populated"].(float64)
	if !ok || (populated != 0 && populated != 1) {
		return fmt.Errorf("invalid populated state")
	}
	procs, ok := p["processes"].([]any)
	if !ok {
		return fmt.Errorf("missing process list")
	}
	if float64(len(procs)) > current {
		return fmt.Errorf("inconsistent process count")
	}
	seen := map[uint64]bool{}
	for _, v := range procs {
		pid, ok := v.(float64)
		if !ok || pid <= 0 || pid != float64(uint64(pid)) || seen[uint64(pid)] {
			return fmt.Errorf("invalid process identity")
		}
		seen[uint64(pid)] = true
	}
	limit, ok := p["pids_max"].(string)
	if !ok || limit == "" {
		return fmt.Errorf("missing pid limit")
	}
	if limit != "max" {
		if n, e := strconv.ParseUint(limit, 10, 64); e != nil || n == 0 {
			return fmt.Errorf("invalid pid limit")
		}
	}
	observed, ok := p["observed_unix_ns"].(string)
	if !ok {
		return fmt.Errorf("missing observed timestamp")
	}
	if n, e := strconv.ParseUint(observed, 10, 64); e != nil || n == 0 {
		return fmt.Errorf("invalid observed timestamp")
	}
	return validateProofState(p, current, populated, procs)
}

func validateAttribution(p map[string]any, target, challenge, boot string) error {
	if p == nil || p["schema_version"] != float64(1) || p["source"] != "kata-agent-runtime-cgroup" || p["container_id"] != target || p["challenge"] != challenge {
		return fmt.Errorf("guest proof attribution mismatch")
	}
	b, ok := p["guest_boot_id"].(string)
	if !ok || b == "" || (boot != "" && b != boot) {
		return fmt.Errorf("guest boot identity missing or changed")
	}
	path, ok := p["cgroup_path"].(string)
	if !ok || !strings.HasPrefix(path, "/sys/fs/cgroup/") || filepath.Clean(path) != path {
		return fmt.Errorf("invalid cgroup path")
	}
	for _, key := range []string{"cgroup_inode", "cgroup_device"} {
		n, ok := p[key].(float64)
		if !ok || n <= 0 || n != float64(uint64(n)) {
			return fmt.Errorf("invalid %s", key)
		}
	}
	return nil
}

func validateProofState(p map[string]any, current, populated float64, procs []any) error {
	switch p["proof_state"] {
	case "live":
	case "cgroup_removed":
		if current != 0 || populated != 0 || len(procs) != 0 || p["removal_confirmed"] != true || p["empty_samples"] != float64(2) {
			return fmt.Errorf("unproven removal")
		}
		var last uint64
		for _, key := range []string{"prior_empty_observed_unix_ns", "observed_unix_ns", "removal_unix_ns", "response_unix_ns"} {
			v, ok := p[key].(string)
			if !ok {
				return fmt.Errorf("missing removal time")
			}
			n, e := strconv.ParseUint(v, 10, 64)
			if e != nil || n == 0 || n < last {
				return fmt.Errorf("invalid removal chronology")
			}
			last = n
		}
	default:
		return fmt.Errorf("unknown proof state")
	}
	return nil
}
