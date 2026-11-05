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
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
)

// WithIdentity publishes a trusted deployment identity, never workload input.
// The supplied node boot identity must match the running supervisor kernel.
func WithIdentity(next http.Handler, raw []byte, token string) (http.Handler, error) {
	var identity struct {
		ProviderID   string `json:"provider_id"`
		ClusterUID   string `json:"cluster_uid"`
		NodeUID      string `json:"node_uid"`
		NodeBootID   string `json:"node_boot_id"`
		PodUID       string `json:"pod_uid"`
		RuntimeEpoch string `json:"runtime_epoch"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&identity); err != nil {
		return nil, err
	}
	if d.Decode(new(any)) != io.EOF {
		return nil, fmt.Errorf("one identity object required")
	}
	for _, s := range []string{identity.ProviderID, identity.ClusterUID, identity.NodeUID, identity.NodeBootID, identity.PodUID, identity.RuntimeEpoch} {
		if len(s) < 3 || len(s) > 128 {
			return nil, fmt.Errorf("incomplete provider identity")
		}
	}
	if os.Getenv("POD_UID") != identity.PodUID {
		return nil, fmt.Errorf("provider identity must match downward API POD_UID")
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return nil, err
	}
	if string(bytes.TrimSpace(boot)) != identity.NodeBootID {
		return nil, fmt.Errorf("node boot identity differs")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/runtime/identity" {
			next.ServeHTTP(w, r)
			return
		}
		if r.Method != "POST" {
			http.Error(w, "method not allowed", 405)
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			http.Error(w, "unauthorized", 401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{"data": identity}); err != nil {
			return
		}
	}), nil
}
