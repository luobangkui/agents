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

package storages

import (
	"crypto/md5" // #nosec G501 -- compatibility identity, not a security digest
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/container-storage-interface/spec/lib/go/csi"
)

// DirectMountTargetHashContextKey carries a precomputed, non-secret target
// identity in a cleanup-only NodePublishVolumeRequest. It lets unmount locate
// the original host target after the source PV has been deleted.
const DirectMountTargetHashContextKey = "agents.kruise.io/direct-mount-target-hash"

// DirectMountTargetHash identifies the real host mount from immutable storage
// semantics. Pod identity fields are excluded so retries and pool reuse remain
// stable. The MD5 shape is retained for compatibility with existing targets.
func DirectMountTargetHash(driverName string, req csi.NodePublishVolumeRequest) string {
	fields := []string{driverName, req.VolumeId, req.TargetPath, fmt.Sprintf("%t", req.Readonly)}
	keys := make([]string, 0, len(req.VolumeContext))
	for key := range req.VolumeContext {
		if isPodIdentityVolumeContextKey(key) || key == DirectMountTargetHashContextKey {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fields = append(fields, key, req.VolumeContext[key])
	}

	var identity strings.Builder
	for _, field := range fields {
		fmt.Fprintf(&identity, "%d:%s;", len(field), field)
	}
	sum := md5.Sum([]byte(identity.String())) // #nosec G401 -- compatibility identity, not security
	return hex.EncodeToString(sum[:])
}

// DirectUnmountTargetHash uses a persisted identity when present and otherwise
// falls back to the legacy derivation. The override is restricted to a canonical
// MD5 hex string so it can never escape the provider mount-root directory.
func DirectUnmountTargetHash(driverName string, req csi.NodePublishVolumeRequest) (string, error) {
	value := strings.TrimSpace(req.VolumeContext[DirectMountTargetHashContextKey])
	if value == "" {
		return DirectMountTargetHash(driverName, req), nil
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != md5.Size || value != strings.ToLower(value) {
		return "", fmt.Errorf("invalid persisted direct mount target hash %q", value)
	}
	return value, nil
}

func isPodIdentityVolumeContextKey(key string) bool {
	switch key {
	case "csi.storage.k8s.io/pod.name",
		"csi.storage.k8s.io/pod.namespace",
		"csi.storage.k8s.io/pod.uid",
		"csi.storage.k8s.io/serviceAccount.name":
		return true
	default:
		return false
	}
}
