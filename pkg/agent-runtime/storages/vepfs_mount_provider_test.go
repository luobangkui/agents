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
	"context"
	"maps"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestVEPFSMountProviderGenerateStagedPlan(t *testing.T) {
	pv := validVEPFSPV()
	originalAttributes := maps.Clone(pv.Spec.CSI.VolumeAttributes)
	provider := &VEPFSMountProvider{}

	plan, err := provider.GenerateStagedCSIMountPlan(
		context.Background(), StagedMountInput{
			TargetPath: "/workspace/data", SubPath: "users/42", PersistentVolume: pv,
		},
	)

	require.NoError(t, err)
	require.Equal(t, VEPFSCSIDriverName, plan.Driver)
	require.Equal(t, MountStrategyKubeletAnchor, plan.Strategy)
	require.NotEmpty(t, plan.VolumeIdentity)
	require.Nil(t, plan.PublishRequest)
	require.Equal(t, PlacementRequirement{
		CSIDriver:   VEPFSCSIDriverName,
		DomainKey:   VEPFSMountServiceDomainKey,
		DomainValue: "mount-aed6284f",
	}, plan.Placement)
	require.Equal(t, &KubeletAnchorSpec{
		PersistentVolumeName: "vepfs-pv",
		PersistentVolumeUID:  types.UID("pv-uid-1"),
		VolumeHandle:         "vepfs-volume-handle",
		SubPath:              "users/42",
		TargetPath:           "/workspace/data",
		ReadOnly:             false,
	}, plan.Anchor)
	require.Equal(t, originalAttributes, pv.Spec.CSI.VolumeAttributes, "provider validation must not mutate the PV")
}

func TestVEPFSMountProviderIdentityIsStable(t *testing.T) {
	provider := &VEPFSMountProvider{}
	pv := validVEPFSPV()

	first, err := provider.GenerateStagedCSIMountPlan(context.Background(), StagedMountInput{
		TargetPath: "/workspace", SubPath: "users/42", PersistentVolume: pv,
	})
	require.NoError(t, err)
	second, err := provider.GenerateStagedCSIMountPlan(context.Background(), StagedMountInput{
		TargetPath: "/workspace", SubPath: "users/42", PersistentVolume: pv.DeepCopy(),
	})
	require.NoError(t, err)
	differentSubPath, err := provider.GenerateStagedCSIMountPlan(context.Background(), StagedMountInput{
		TargetPath: "/workspace", SubPath: "users/43", PersistentVolume: pv,
	})
	require.NoError(t, err)

	require.Equal(t, first.VolumeIdentity, second.VolumeIdentity)
	require.NotEqual(t, first.VolumeIdentity, differentSubPath.VolumeIdentity)
}

func TestVEPFSMountProviderRejectsInvalidPV(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*corev1.PersistentVolume)
		wantErr string
	}{
		{name: "missing CSI", mutate: func(pv *corev1.PersistentVolume) { pv.Spec.CSI = nil }, wantErr: "CSI"},
		{name: "wrong driver", mutate: func(pv *corev1.PersistentVolume) {
			pv.Spec.CSI.Driver = "other.csi.example.com"
		}, wantErr: "driver"},
		{name: "missing volume handle", mutate: func(pv *corev1.PersistentVolume) {
			pv.Spec.CSI.VolumeHandle = ""
		}, wantErr: "volume handle"},
		{name: "missing PV UID", mutate: func(pv *corev1.PersistentVolume) {
			pv.UID = ""
		}, wantErr: "volume UID"},
		{name: "missing fsid", mutate: func(pv *corev1.PersistentVolume) {
			delete(pv.Spec.CSI.VolumeAttributes, VEPFSAttributeFSID)
		}, wantErr: "fsid"},
		{name: "missing mount service", mutate: func(pv *corev1.PersistentVolume) {
			delete(pv.Spec.CSI.VolumeAttributes, VEPFSAttributeMountServiceID)
		}, wantErr: "mountServiceID"},
		{name: "single node access", mutate: func(pv *corev1.PersistentVolume) {
			pv.Spec.AccessModes = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}
		}, wantErr: "multi-node"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pv := validVEPFSPV()
			tt.mutate(pv)

			_, err := (&VEPFSMountProvider{}).GenerateStagedCSIMountPlan(
				context.Background(), StagedMountInput{TargetPath: "/workspace", PersistentVolume: pv},
			)

			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestVEPFSMountProviderRejectsDirectPublish(t *testing.T) {
	_, err := (&VEPFSMountProvider{}).GenerateCSINodePublishVolumeRequest(
		context.Background(), "/workspace", validVEPFSPV(), false, nil,
	)

	require.ErrorContains(t, err, "kubelet anchor")
}

func validVEPFSPV() *corev1.PersistentVolume {
	return &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "vepfs-pv", UID: types.UID("pv-uid-1")},
		Spec: corev1.PersistentVolumeSpec{
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				CSI: &corev1.CSIPersistentVolumeSource{
					Driver:       VEPFSCSIDriverName,
					VolumeHandle: "vepfs-volume-handle",
					VolumeAttributes: map[string]string{
						VEPFSAttributeFSID:           "fs-vepfs-01",
						VEPFSAttributeMountServiceID: "mount-aed6284f",
					},
				},
			},
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
		},
	}
}
