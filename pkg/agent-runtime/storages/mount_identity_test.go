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
	"strings"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/stretchr/testify/require"
)

func TestDirectMountTargetHash(t *testing.T) {
	base := csi.NodePublishVolumeRequest{
		VolumeId:   "volume-a",
		TargetPath: "/workspace/data",
		VolumeContext: map[string]string{
			"server":                         "storage.example.com",
			"csi.storage.k8s.io/pod.uid":    "pod-a",
			"csi.storage.k8s.io/pod.name":   "sandbox-a",
			"csi.storage.k8s.io/pod.namespace": "default",
		},
	}

	t.Run("ignores pod incarnation", func(t *testing.T) {
		changed := base
		changed.VolumeContext = map[string]string{}
		for key, value := range base.VolumeContext {
			changed.VolumeContext[key] = value
		}
		changed.VolumeContext["csi.storage.k8s.io/pod.uid"] = "pod-b"
		changed.VolumeContext["csi.storage.k8s.io/pod.name"] = "sandbox-b"
		require.Equal(t, DirectMountTargetHash("driver.example", base), DirectMountTargetHash("driver.example", changed))
	})

	t.Run("changes storage semantics", func(t *testing.T) {
		changed := base
		changed.VolumeContext = map[string]string{"server": "other.example.com"}
		require.NotEqual(t, DirectMountTargetHash("driver.example", base), DirectMountTargetHash("driver.example", changed))
	})
}

func TestDirectUnmountTargetHash(t *testing.T) {
	hash := strings.Repeat("a", 32)
	tests := []struct {
		name    string
		value   string
		want    string
		wantErr bool
	}{
		{name: "persisted hash", value: hash, want: hash},
		{name: "uppercase rejected", value: strings.ToUpper(hash), wantErr: true},
		{name: "invalid length rejected", value: "abc", wantErr: true},
		{name: "path rejected", value: "../../outside", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := DirectUnmountTargetHash("driver.example", csi.NodePublishVolumeRequest{
				VolumeContext: map[string]string{DirectMountTargetHashContextKey: tt.value},
			})
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}
