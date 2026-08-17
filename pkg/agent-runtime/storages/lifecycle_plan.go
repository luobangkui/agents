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
	"encoding/json"
	"fmt"

	"github.com/container-storage-interface/spec/lib/go/csi"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

// MountStrategy describes which component owns the CSI lifecycle for a
// resolved mount. It is capability-based so drivers from different vendors can
// share one execution strategy.
type MountStrategy string

const (
	// MountStrategyDirectNodePublish executes NodePublishVolume from the
	// sandbox runtime. This is the legacy NAS/OSS-compatible strategy.
	MountStrategyDirectNodePublish MountStrategy = "DirectNodePublish"
	// MountStrategyKubeletAnchor delegates attach, stage, and publish to kubelet
	// through a same-node Kubernetes anchor before a node mounter bind-mounts it
	// into the running Sandbox.
	MountStrategyKubeletAnchor MountStrategy = "KubeletAnchor"
)

// PlacementRequirement is the driver capability a Sandbox node must satisfy
// before a Claim is allowed to choose it.
type PlacementRequirement struct {
	CSIDriver   string
	DomainKey   string
	DomainValue string
}

// KubeletAnchorSpec is a provider-neutral description of the Kubernetes volume
// anchor required by a staged CSI driver. Secret references deliberately remain
// on the PV and are consumed by Kubernetes; they are not copied into this plan.
type KubeletAnchorSpec struct {
	PersistentVolumeName           string
	PersistentVolumeUID            types.UID
	PersistentVolumeClaimNamespace string
	PersistentVolumeClaimName      string
	PersistentVolumeClaimUID       types.UID
	VolumeHandle                   string
	SubPath                        string
	TargetPath                     string
	ReadOnly                       bool
}

// MountPlan is the resolved lifecycle intent. Exactly one of PublishRequest and
// Anchor is populated according to Strategy.
type MountPlan struct {
	Driver         string
	Strategy       MountStrategy
	VolumeIdentity string
	PublishRequest *csi.NodePublishVolumeRequest
	Anchor         *KubeletAnchorSpec
	Placement      PlacementRequirement
}

const redactedLifecycleRequest = "[redacted]"

type mountPlanView struct {
	Driver         string        `json:"driver"`
	Strategy       MountStrategy `json:"strategy"`
	VolumeIdentity string        `json:"volumeIdentity"`
	PublishRequest string        `json:"publishRequest,omitempty"`
}

// MarshalJSON prevents CSI Secrets from leaking when a plan is nested in an
// options struct logged through encoding/json.
func (p MountPlan) MarshalJSON() ([]byte, error) {
	view := mountPlanView{
		Driver:         p.Driver,
		Strategy:       p.Strategy,
		VolumeIdentity: p.VolumeIdentity,
	}
	if p.PublishRequest != nil {
		view.PublishRequest = redactedLifecycleRequest
	}
	return json.Marshal(view)
}

// String prevents the generated CSI message String method from rendering
// Secrets in logs.
func (p MountPlan) String() string {
	publishRequest := ""
	if p.PublishRequest != nil {
		publishRequest = redactedLifecycleRequest
	}
	return fmt.Sprintf("MountPlan{Driver:%s, Strategy:%s, VolumeIdentity:%s, PublishRequest:%s}",
		p.Driver, p.Strategy, p.VolumeIdentity, publishRequest)
}

// StagedMountInput contains the Kubernetes-owned inputs required to resolve an
// anchor plan. It remains free of NodePublish Secrets because kubelet reads the
// PV's lifecycle Secret references directly.
type StagedMountInput struct {
	TargetPath       string
	SubPath          string
	PersistentVolume *corev1.PersistentVolume
	ReadOnly         bool
}

// StagedVolumeMountProvider is an optional capability implemented only by
// drivers whose lifecycle cannot be represented by a standalone
// NodePublishVolume request.
type StagedVolumeMountProvider interface {
	VolumeMountProvider
	GenerateStagedCSIMountPlan(
		ctx context.Context,
		input StagedMountInput,
	) (*MountPlan, error)
}
