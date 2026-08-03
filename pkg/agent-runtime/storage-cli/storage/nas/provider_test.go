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

package nas

import (
	"maps"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/stretchr/testify/require"
)

func TestProviderValidate(t *testing.T) {
	valid := csi.NodePublishVolumeRequest{
		VolumeId:   "nas-pv-1",
		TargetPath: "/data",
		VolumeCapability: &csi.VolumeCapability{
			AccessType: &csi.VolumeCapability_Mount{
				Mount: &csi.VolumeCapability_MountVolume{},
			},
		},
		VolumeContext: map[string]string{
			"server": "105644841b-irg6.cn-beijing.nas.aliyuncs.com",
			"path":   "/bohr-sandbox/users/14045",
		},
	}

	tests := []struct {
		name    string
		mutate  func(*csi.NodePublishVolumeRequest)
		wantErr string
	}{
		{name: "valid"},
		{name: "missing volume ID", mutate: func(req *csi.NodePublishVolumeRequest) {
			req.VolumeId = ""
		}, wantErr: "volume ID"},
		{name: "missing target", mutate: func(req *csi.NodePublishVolumeRequest) {
			req.TargetPath = ""
		}, wantErr: "target path"},
		{name: "missing capability", mutate: func(req *csi.NodePublishVolumeRequest) {
			req.VolumeCapability = nil
		}, wantErr: "volume capability"},
		{name: "missing server", mutate: func(req *csi.NodePublishVolumeRequest) {
			delete(req.VolumeContext, "server")
		}, wantErr: "server"},
		{name: "missing path", mutate: func(req *csi.NodePublishVolumeRequest) {
			delete(req.VolumeContext, "path")
		}, wantErr: "path"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := valid
			req.VolumeContext = maps.Clone(valid.VolumeContext)
			if tt.mutate != nil {
				tt.mutate(&req)
			}
			err := (&provider{}).Validate(req)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestProviderIdentity(t *testing.T) {
	p := &provider{}
	require.Equal(t, driverName, p.Driver())
	require.Equal(t, "nas", p.SubDir())
}
