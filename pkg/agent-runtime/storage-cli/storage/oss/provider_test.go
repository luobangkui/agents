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

package oss

import (
	"context"
	"maps"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/stretchr/testify/require"
)

func TestProviderValidate(t *testing.T) {
	valid := csi.NodePublishVolumeRequest{
		VolumeId:   "oss-pv-1",
		TargetPath: "/data-oss",
		VolumeCapability: &csi.VolumeCapability{
			AccessType: &csi.VolumeCapability_Mount{
				Mount: &csi.VolumeCapability_MountVolume{},
			},
		},
		VolumeContext: map[string]string{
			"bucket": "bohr-sandbox-test",
			"url":    "oss-cn-beijing.aliyuncs.com",
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
		{name: "missing bucket", mutate: func(req *csi.NodePublishVolumeRequest) {
			delete(req.VolumeContext, "bucket")
		}, wantErr: "bucket"},
		{name: "missing url", mutate: func(req *csi.NodePublishVolumeRequest) {
			delete(req.VolumeContext, "url")
		}, wantErr: "url"},
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
	require.Equal(t, "oss", p.SubDir())
}

func TestProviderUnmountUsesCSIUnpublish(t *testing.T) {
	req := csi.NodePublishVolumeRequest{VolumeId: "oss-pv-1", TargetPath: "/real/oss/target"}
	originalRunner := runNodeUnpublishVolumeFn
	runNodeUnpublishVolumeFn = func(ctx context.Context, driver string, got csi.NodePublishVolumeRequest) error {
		require.NotNil(t, ctx)
		require.Equal(t, driverName, driver)
		require.Equal(t, req, got)
		return nil
	}
	t.Cleanup(func() { runNodeUnpublishVolumeFn = originalRunner })

	require.NoError(t, (&provider{}).Unmount(context.Background(), req))
}
