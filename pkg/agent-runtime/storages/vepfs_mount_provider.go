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
	"crypto/sha256"
	"fmt"
	"path"
	"strconv"
	"strings"

	"github.com/container-storage-interface/spec/lib/go/csi"
	corev1 "k8s.io/api/core/v1"
)

const (
	VEPFSCSIDriverName           = "vepfs.csi.volcengine.com"
	VEPFSAttributeFSID           = "fsid"
	VEPFSAttributeMountServiceID = "mountServiceID"
	VEPFSMountServiceDomainKey   = "vepfs.csi.volcengine.com/mount-service"
)

// VEPFSMountProvider resolves VEPFS as a kubelet-owned staged lifecycle. It
// intentionally refuses direct NodePublish generation because the driver reads
// metadata created by NodeStageVolume.
type VEPFSMountProvider struct{}

func (p *VEPFSMountProvider) GenerateCSINodePublishVolumeRequest(
	context.Context,
	string,
	*corev1.PersistentVolume,
	bool,
	*corev1.Secret,
) (*csi.NodePublishVolumeRequest, error) {
	return nil, fmt.Errorf("VEPFS requires the kubelet anchor lifecycle; direct NodePublishVolume is unsupported")
}

func (p *VEPFSMountProvider) GenerateStagedCSIMountPlan(
	_ context.Context,
	input StagedMountInput,
) (*MountPlan, error) {
	persistentVolumeObj := input.PersistentVolume
	if persistentVolumeObj == nil {
		return nil, fmt.Errorf("persistent volume object is nil")
	}
	if persistentVolumeObj.Spec.CSI == nil {
		return nil, fmt.Errorf("VEPFS persistent volume has no CSI source")
	}
	source := persistentVolumeObj.Spec.CSI
	if source.Driver != VEPFSCSIDriverName {
		return nil, fmt.Errorf("unexpected VEPFS CSI driver %q", source.Driver)
	}
	volumeHandle := strings.TrimSpace(source.VolumeHandle)
	if volumeHandle == "" {
		return nil, fmt.Errorf("VEPFS CSI volume handle is required")
	}
	fsid := strings.TrimSpace(source.VolumeAttributes[VEPFSAttributeFSID])
	if fsid == "" {
		return nil, fmt.Errorf("VEPFS volume attribute %q is required", VEPFSAttributeFSID)
	}
	mountServiceID := strings.TrimSpace(source.VolumeAttributes[VEPFSAttributeMountServiceID])
	if mountServiceID == "" {
		return nil, fmt.Errorf("VEPFS volume attribute %q is required", VEPFSAttributeMountServiceID)
	}
	if strings.TrimSpace(persistentVolumeObj.Name) == "" {
		return nil, fmt.Errorf("VEPFS persistent volume name is required")
	}
	if persistentVolumeObj.UID == "" {
		return nil, fmt.Errorf("VEPFS persistent volume UID is required")
	}
	claimRef := persistentVolumeObj.Spec.ClaimRef
	if claimRef == nil || strings.TrimSpace(claimRef.Namespace) == "" || strings.TrimSpace(claimRef.Name) == "" {
		return nil, fmt.Errorf("VEPFS persistent volume must be bound to a namespaced persistent volume claim")
	}
	if strings.TrimSpace(input.TargetPath) == "" {
		return nil, fmt.Errorf("VEPFS target path is required")
	}
	if !supportsMultiNodeAccess(persistentVolumeObj.Spec.AccessModes) {
		return nil, fmt.Errorf("VEPFS dynamic anchor requires a multi-node PV access mode")
	}
	cleanSubPath, err := cleanAnchorSubPath(input.SubPath)
	if err != nil {
		return nil, err
	}

	effectiveReadOnly := input.ReadOnly || IsPureReadOnly(persistentVolumeObj.Spec.AccessModes)
	identity := stableMountIdentity(
		VEPFSCSIDriverName,
		string(persistentVolumeObj.UID),
		volumeHandle,
		cleanSubPath,
		input.TargetPath,
		strconv.FormatBool(effectiveReadOnly),
	)
	return &MountPlan{
		Driver:         VEPFSCSIDriverName,
		Strategy:       MountStrategyKubeletAnchor,
		VolumeIdentity: identity,
		Anchor: &KubeletAnchorSpec{
			PersistentVolumeName:           persistentVolumeObj.Name,
			PersistentVolumeUID:            persistentVolumeObj.UID,
			PersistentVolumeClaimNamespace: claimRef.Namespace,
			PersistentVolumeClaimName:      claimRef.Name,
			PersistentVolumeClaimUID:       claimRef.UID,
			VolumeHandle:                   volumeHandle,
			SubPath:                        cleanSubPath,
			TargetPath:                     input.TargetPath,
			ReadOnly:                       effectiveReadOnly,
		},
		Placement: PlacementRequirement{
			CSIDriver:   VEPFSCSIDriverName,
			DomainKey:   VEPFSMountServiceDomainKey,
			DomainValue: mountServiceID,
		},
	}, nil
}

func supportsMultiNodeAccess(modes []corev1.PersistentVolumeAccessMode) bool {
	for _, mode := range modes {
		if mode == corev1.ReadWriteMany || mode == corev1.ReadOnlyMany {
			return true
		}
	}
	return false
}

func cleanAnchorSubPath(subPath string) (string, error) {
	if subPath == "" {
		return "", nil
	}
	if strings.ContainsRune(subPath, '\x00') {
		return "", fmt.Errorf("VEPFS sub-path contains a null byte")
	}
	if path.IsAbs(subPath) {
		return "", fmt.Errorf("VEPFS sub-path must be relative")
	}
	cleaned := path.Clean(subPath)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("VEPFS sub-path must remain within the volume")
	}
	return cleaned, nil
}

func stableMountIdentity(fields ...string) string {
	digest := sha256.New()
	for _, field := range fields {
		fmt.Fprintf(digest, "%d:%s;", len(field), field)
	}
	return fmt.Sprintf("%x", digest.Sum(nil))
}

var _ StagedVolumeMountProvider = (*VEPFSMountProvider)(nil)
