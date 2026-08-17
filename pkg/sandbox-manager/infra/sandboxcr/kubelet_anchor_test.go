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
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	agentsv1alpha1 "github.com/openkruise/agents/api/v1alpha1"
	"github.com/openkruise/agents/pkg/agent-runtime/storages"
	runtimeclient "github.com/openkruise/agents/pkg/utils/runtime"
	runtimeconfig "github.com/openkruise/agents/pkg/utils/runtime/config"
)

func TestCandidateSupportsStagedMounts(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, storagev1.AddToScheme(scheme))
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-a", Labels: map[string]string{
		storages.VEPFSMountServiceDomainKey: "mount-aed6284f",
	}}}
	propagation := corev1.MountPropagationHostToContainer
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "sandbox-a", UID: types.UID("pod-uid")},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "main", VolumeMounts: []corev1.VolumeMount{{
				Name: sandboxMountRootName, MountPath: sandboxMountRootPath, MountPropagation: &propagation,
			}}}},
			Volumes: []corev1.Volume{{Name: sandboxMountRootName, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}},
		},
	}
	csiNode := &storagev1.CSINode{ObjectMeta: metav1.ObjectMeta{Name: "node-a"}, Spec: storagev1.CSINodeSpec{
		Drivers: []storagev1.CSINodeDriver{{Name: storages.VEPFSCSIDriverName, NodeID: "node-a"}},
	}}
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(node, csiNode, pod).Build()
	sbx := &agentsv1alpha1.Sandbox{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "sandbox-a"},
		Status:     agentsv1alpha1.SandboxStatus{PodInfo: agentsv1alpha1.PodInfo{NodeName: "node-a", PodUID: pod.UID}},
	}
	opts := &runtimeconfig.CSIMountOptions{StagedMountOptionList: []runtimeconfig.StagedMountConfig{{Plan: storages.MountPlan{
		Placement: storages.PlacementRequirement{
			CSIDriver: storages.VEPFSCSIDriverName, DomainKey: storages.VEPFSMountServiceDomainKey, DomainValue: "mount-aed6284f",
		},
	}}}}

	require.NoError(t, candidateSupportsStagedMounts(context.Background(), reader, sbx, opts))
	pod.Spec.Containers[0].VolumeMounts[0].MountPropagation = nil
	require.NoError(t, reader.Update(context.Background(), pod))
	require.ErrorContains(t, candidateSupportsStagedMounts(context.Background(), reader, sbx, opts), "lacks mount-root HostToContainer")
	pod.Spec.Containers[0].VolumeMounts[0].MountPropagation = &propagation
	require.NoError(t, reader.Update(context.Background(), pod))
	node.Labels[storages.VEPFSMountServiceDomainKey] = "other-domain"
	require.NoError(t, reader.Update(context.Background(), node))
	require.ErrorContains(t, candidateSupportsStagedMounts(context.Background(), reader, sbx, opts), "requires")
}

func TestApplyStagedPlacementToNewSandbox(t *testing.T) {
	newSandbox := func() *agentsv1alpha1.Sandbox {
		return &agentsv1alpha1.Sandbox{Spec: agentsv1alpha1.SandboxSpec{
			EmbeddedSandboxTemplate: agentsv1alpha1.EmbeddedSandboxTemplate{
				TemplateRef: &agentsv1alpha1.SandboxTemplateRef{Name: "resolved-template"},
				Template: &corev1.PodTemplateSpec{Spec: corev1.PodSpec{
					NodeSelector: map[string]string{"existing": "value"},
				}},
			},
		}}
	}
	options := func(key, value string) *runtimeconfig.CSIMountOptions {
		return &runtimeconfig.CSIMountOptions{StagedMountOptionList: []runtimeconfig.StagedMountConfig{{Plan: storages.MountPlan{
			Driver: "vepfs.csi.volcengine.com",
			Placement: storages.PlacementRequirement{
				DomainKey: key, DomainValue: value,
			},
		}}}}
	}

	t.Run("materializes template and adds selector", func(t *testing.T) {
		sbx := newSandbox()
		err := applyStagedPlacementToNewSandbox(sbx, options("vepfs.csi.volcengine.com/mount-service", "mount-aed6284f"))
		require.NoError(t, err)
		require.Nil(t, sbx.Spec.TemplateRef)
		require.Equal(t, "value", sbx.Spec.Template.Spec.NodeSelector["existing"])
		require.Equal(t, "mount-aed6284f", sbx.Spec.Template.Spec.NodeSelector["vepfs.csi.volcengine.com/mount-service"])
	})

	t.Run("rejects existing selector conflict", func(t *testing.T) {
		sbx := newSandbox()
		sbx.Spec.Template.Spec.NodeSelector["vepfs.csi.volcengine.com/mount-service"] = "mount-other"
		err := applyStagedPlacementToNewSandbox(sbx, options("vepfs.csi.volcengine.com/mount-service", "mount-aed6284f"))
		require.ErrorContains(t, err, "conflicts with node selector")
	})

	t.Run("rejects different domains in one claim", func(t *testing.T) {
		sbx := newSandbox()
		opts := options("vepfs.csi.volcengine.com/mount-service", "mount-aed6284f")
		opts.StagedMountOptionList = append(opts.StagedMountOptionList, runtimeconfig.StagedMountConfig{Plan: storages.MountPlan{
			Driver: "vepfs.csi.volcengine.com",
			Placement: storages.PlacementRequirement{
				DomainKey: "vepfs.csi.volcengine.com/mount-service", DomainValue: "mount-other",
			},
		}})
		err := applyStagedPlacementToNewSandbox(sbx, opts)
		require.ErrorContains(t, err, "conflicts with node selector")
	})

	t.Run("requires resolved template", func(t *testing.T) {
		sbx := &agentsv1alpha1.Sandbox{}
		err := applyStagedPlacementToNewSandbox(sbx, options("vepfs.csi.volcengine.com/mount-service", "mount-aed6284f"))
		require.ErrorContains(t, err, "resolved pod template is required")
	})
}

func TestReconcileKubeletAnchorsReapsStalePodIncarnation(t *testing.T) {
	infraInstance, kubeClient := NewTestInfra(t)
	defer infraInstance.Stop(t.Context())

	sbx := &agentsv1alpha1.Sandbox{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "claimed-sandbox",
			UID:       types.UID("sandbox-uid"),
			Labels:    map[string]string{agentsv1alpha1.LabelSandboxIsClaimed: agentsv1alpha1.True},
		},
		Status: agentsv1alpha1.SandboxStatus{PodInfo: agentsv1alpha1.PodInfo{PodUID: types.UID("new-pod-uid")}},
	}
	require.NoError(t, kubeClient.Create(t.Context(), sbx))

	anchor := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:  "default",
			Name:       "csi-anchor-stale",
			Finalizers: []string{KubeletAnchorFinalizer},
			Labels: map[string]string{
				KubeletAnchorManagedLabel:     "true",
				KubeletAnchorSandboxNameLabel: sbx.Name,
				KubeletAnchorSandboxNSLabel:   sbx.Namespace,
			},
			Annotations: map[string]string{
				KubeletAnchorSandboxUIDAnno:    string(sbx.UID),
				KubeletAnchorSandboxPodUIDAnno: "old-pod-uid",
			},
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: KubeletAnchorContainerName, Image: "anchor:test"}}},
	}
	require.NoError(t, kubeClient.Create(t.Context(), anchor))
	anchor.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: KubeletAnchorContainerName,
		State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			ExitCode: 0,
		}},
	}}
	require.NoError(t, kubeClient.Status().Update(t.Context(), anchor))

	require.NoError(t, infraInstance.reconcileKubeletAnchors(t.Context()))
	require.NoError(t, kubeClient.Get(t.Context(), client.ObjectKeyFromObject(anchor), &corev1.Pod{}),
		"the first cache-derived stale observation must not delete an anchor")
	infraInstance.kubeletAnchorStaleObservations.Store(
		client.ObjectKeyFromObject(anchor), time.Now().Add(-kubeletAnchorOrphanGrace),
	)
	require.NoError(t, infraInstance.reconcileKubeletAnchors(t.Context()))
	err := kubeClient.Get(t.Context(), client.ObjectKeyFromObject(anchor), &corev1.Pod{})
	require.True(t, apierrors.IsNotFound(err), "stale anchor should be deleted, got %v", err)
}

func TestConfirmKubeletAnchorStaleRequiresContinuousGrace(t *testing.T) {
	infraInstance := &Infra{}
	key := client.ObjectKey{Namespace: "default", Name: "anchor"}
	now := time.Now()

	require.False(t, infraInstance.confirmKubeletAnchorStale(key, now))
	require.False(t, infraInstance.confirmKubeletAnchorStale(key, now.Add(kubeletAnchorOrphanGrace-time.Nanosecond)))
	require.True(t, infraInstance.confirmKubeletAnchorStale(key, now.Add(kubeletAnchorOrphanGrace)))

	infraInstance.clearKubeletAnchorStaleObservation(key)
	require.False(t, infraInstance.confirmKubeletAnchorStale(key, now.Add(2*kubeletAnchorOrphanGrace)),
		"a healthy observation must reset the grace window")
}

func TestBuildKubeletAnchorPod(t *testing.T) {
	t.Setenv(KubeletAnchorImageEnv, "registry.example/anchor:test")
	t.Setenv(KubeletAnchorPullSecretEnv, "registry-secret")
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Namespace: "storage", Name: "vepfs-pvc", UID: types.UID("pvc-uid")},
		Spec:       corev1.PersistentVolumeClaimSpec{VolumeName: "vepfs-pv"},
	}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pvc).Build()
	sbx := &agentsv1alpha1.Sandbox{
		ObjectMeta: metav1.ObjectMeta{Namespace: "sandbox", Name: "warm-sandbox", UID: types.UID("sandbox-uid")},
		Status: agentsv1alpha1.SandboxStatus{PodInfo: agentsv1alpha1.PodInfo{
			NodeName: "node-a", PodUID: types.UID("sandbox-pod-uid"),
		}},
	}
	identity := strings.Repeat("a", 64)
	plan := &storages.MountPlan{
		Strategy:       storages.MountStrategyKubeletAnchor,
		VolumeIdentity: identity,
		Anchor: &storages.KubeletAnchorSpec{
			PersistentVolumeName:           "vepfs-pv",
			PersistentVolumeClaimNamespace: "storage",
			PersistentVolumeClaimName:      "vepfs-pvc",
			PersistentVolumeClaimUID:       types.UID("pvc-uid"),
			SubPath:                        "users/42",
			TargetPath:                     "/workspace/data",
		},
	}

	pod, err := buildKubeletAnchorPod(context.Background(), kubeClient, sbx, plan)
	require.NoError(t, err)
	require.Equal(t, "storage", pod.Namespace)
	require.Equal(t, "node-a", pod.Spec.NodeName)
	require.NotNil(t, pod.Spec.AutomountServiceAccountToken)
	require.False(t, *pod.Spec.AutomountServiceAccountToken)
	require.Contains(t, pod.Finalizers, KubeletAnchorFinalizer)
	require.Equal(t, kubeletAnchorExposureUnexposed, pod.Annotations[KubeletAnchorExposureStateAnno])
	require.Equal(t, "registry.example/anchor:test", pod.Spec.Containers[0].Image)
	require.Equal(t, "users/42", pod.Spec.Containers[0].VolumeMounts[0].SubPath)
	require.Equal(t, corev1.MountPropagationBidirectional, *pod.Spec.Containers[0].VolumeMounts[1].MountPropagation)
	require.Equal(t,
		"/var/lib/kubelet/pods/sandbox-pod-uid/volumes/kubernetes.io~empty-dir/mount-root",
		pod.Spec.Volumes[1].HostPath.Path)
	require.Equal(t, "registry-secret", pod.Spec.ImagePullSecrets[0].Name)

	otherSandbox := sbx.DeepCopy()
	otherSandbox.UID = types.UID("other-sandbox-uid")
	otherPod, err := buildKubeletAnchorPod(context.Background(), kubeClient, otherSandbox, plan)
	require.NoError(t, err)
	require.NotEqual(t, pod.Name, otherPod.Name, "concurrent sandboxes sharing an RWX volume need distinct anchor pods")
}

func TestPendingExposureExpired(t *testing.T) {
	now := time.Now().UTC()
	tests := []struct {
		name        string
		annotations map[string]string
		want        bool
	}{
		{name: "ready is not pending", annotations: map[string]string{KubeletAnchorExposureStateAnno: kubeletAnchorExposureReady}},
		{name: "recent pending", annotations: map[string]string{
			KubeletAnchorExposureStateAnno:  kubeletAnchorExposurePending,
			KubeletAnchorExposureUpdateAnno: now.Add(-time.Minute).Format(time.RFC3339Nano),
		}},
		{name: "expired pending", annotations: map[string]string{
			KubeletAnchorExposureStateAnno:  kubeletAnchorExposurePending,
			KubeletAnchorExposureUpdateAnno: now.Add(-defaultKubeletAnchorTimeout).Format(time.RFC3339Nano),
		}, want: true},
		{name: "invalid timestamp is repaired", annotations: map[string]string{
			KubeletAnchorExposureStateAnno:  kubeletAnchorExposurePending,
			KubeletAnchorExposureUpdateAnno: "invalid",
		}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: tt.annotations}}
			require.Equal(t, tt.want, pendingExposureExpired(pod, now))
		})
	}
}

func TestCleanupDynamicMountsUnmountsDirectProviders(t *testing.T) {
	infraInstance, kubeClient := NewTestInfra(t)
	defer infraInstance.Stop(t.Context())
	const driver = "direct.test.csi.example.com"
	infraInstance.StorageRegistry.RegisterProvider(driver, &storages.MountProvider{})

	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "direct-pv"},
		Spec: corev1.PersistentVolumeSpec{PersistentVolumeSource: corev1.PersistentVolumeSource{
			CSI: &corev1.CSIPersistentVolumeSource{
				Driver:           driver,
				VolumeHandle:     "stable-volume-handle",
				VolumeAttributes: map[string]string{"server": "storage.example.com", "path": "/share"},
			},
		}},
	}
	require.NoError(t, kubeClient.Create(t.Context(), pv))
	sbx := &agentsv1alpha1.Sandbox{ObjectMeta: metav1.ObjectMeta{
		Namespace: "default",
		Name:      "direct-mount-sandbox",
		UID:       types.UID("sandbox-uid"),
		Annotations: map[string]string{
			agentsv1alpha1.AnnotationCSIVolumeConfig: `[{"pvName":"direct-pv","mountPath":"/workspace/data"}]`,
		},
	}}
	resolved, err := runtimeclient.ResolveCSIMountFromAnnotation(
		t.Context(), sbx, infraInstance.Cache.GetClient(), infraInstance.StorageRegistry,
	)
	require.NoError(t, err)
	expectedHash := storages.DirectMountTargetHash(driver, *resolved.MountOptionList[0].PublishRequest)
	require.NoError(t, kubeClient.Create(t.Context(), sbx))
	callerSnapshot := sbx.DeepCopy()
	require.NoError(t, runtimeclient.RecordDirectCSIUnmounts(callerSnapshot, resolved))
	require.NoError(t, kubeClient.Delete(t.Context(), pv),
		"cleanup identity must remain usable after the source PV is deleted")

	originalProcess := processCSIUnmounts
	t.Cleanup(func() { processCSIUnmounts = originalProcess })
	called := false
	processCSIUnmounts = func(_ context.Context, gotSandbox *agentsv1alpha1.Sandbox, opts runtimeconfig.CSIMountOptions, _ ...runtimeclient.Option) (time.Duration, error) {
		called = true
		require.Equal(t, sbx.Name, gotSandbox.Name)
		require.Len(t, opts.MountOptionList, 1)
		require.Equal(t, driver, opts.MountOptionList[0].Driver)
		require.Equal(t, "stable-volume-handle", opts.MountOptionList[0].PublishRequest.GetVolumeId())
		actualHash, hashErr := storages.DirectUnmountTargetHash(driver, *opts.MountOptionList[0].PublishRequest)
		require.NoError(t, hashErr)
		require.Equal(t, expectedHash, actualHash)
		return 0, nil
	}

	require.NoError(t, infraInstance.CleanupDynamicMounts(t.Context(), AsSandbox(callerSnapshot, infraInstance.Cache)))
	require.True(t, called, "direct CSI cleanup must reach ProcessCSIUnmounts")
}
