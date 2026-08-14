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
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	agentsv1alpha1 "github.com/openkruise/agents/api/v1alpha1"
	"github.com/openkruise/agents/pkg/agent-runtime/storages"
	managerinfra "github.com/openkruise/agents/pkg/sandbox-manager/infra"
	runtimeclient "github.com/openkruise/agents/pkg/utils/runtime"
	"github.com/openkruise/agents/pkg/utils/runtime/config"
)

const (
	KubeletAnchorImageEnv           = "KUBELET_ANCHOR_MOUNTER_IMAGE"
	KubeletAnchorPullSecretEnv      = "KUBELET_ANCHOR_IMAGE_PULL_SECRET"
	KubeletAnchorManagedLabel       = "agents.kruise.io/kubelet-anchor"
	KubeletAnchorSandboxNameLabel   = "agents.kruise.io/anchor-sandbox-name"
	KubeletAnchorSandboxNSLabel     = "agents.kruise.io/anchor-sandbox-namespace"
	KubeletAnchorIdentityAnnotation = "agents.kruise.io/anchor-identity"
	KubeletAnchorTargetAnnotation   = "agents.kruise.io/anchor-target-path"
	KubeletAnchorSandboxUIDAnno     = "agents.kruise.io/anchor-sandbox-uid"
	KubeletAnchorSandboxPodUIDAnno  = "agents.kruise.io/anchor-sandbox-pod-uid"
	KubeletAnchorPVAnnotation       = "agents.kruise.io/anchor-pv"
	KubeletAnchorFinalizer          = "agents.kruise.io/kubelet-anchor-cleanup"
	KubeletAnchorContainerName      = "kubelet-anchor-mounter"

	defaultKubeletAnchorTimeout  = 2 * time.Minute
	kubeletAnchorReconcilePeriod = 30 * time.Second
	kubeletPodsRoot              = "/var/lib/kubelet/pods"
	anchorSourcePath             = "/source"
	anchorTargetRoot             = "/target"
)

func candidateSupportsStagedMounts(
	ctx context.Context,
	reader client.Reader,
	sbx *agentsv1alpha1.Sandbox,
	opts *config.CSIMountOptions,
) error {
	if opts == nil || len(opts.StagedMountOptionList) == 0 {
		return nil
	}
	nodeName := strings.TrimSpace(sbx.Status.PodInfo.NodeName)
	if nodeName == "" {
		return fmt.Errorf("sandbox has no assigned node")
	}
	node := &corev1.Node{}
	if err := reader.Get(ctx, client.ObjectKey{Name: nodeName}, node); err != nil {
		return fmt.Errorf("get sandbox node %s: %w", nodeName, err)
	}
	csiNode := &storagev1.CSINode{}
	if err := reader.Get(ctx, client.ObjectKey{Name: nodeName}, csiNode); err != nil {
		return fmt.Errorf("get CSINode %s: %w", nodeName, err)
	}

	for _, mount := range opts.StagedMountOptionList {
		requirement := mount.Plan.Placement
		if requirement.DomainKey != "" && node.Labels[requirement.DomainKey] != requirement.DomainValue {
			return fmt.Errorf("node %s has %s=%q, requires %q", nodeName, requirement.DomainKey,
				node.Labels[requirement.DomainKey], requirement.DomainValue)
		}
		if !csiNodeHasDriver(csiNode, requirement.CSIDriver) {
			return fmt.Errorf("node %s does not register CSI driver %s", nodeName, requirement.CSIDriver)
		}
	}
	return nil
}

func csiNodeHasDriver(csiNode *storagev1.CSINode, driver string) bool {
	for _, installed := range csiNode.Spec.Drivers {
		if installed.Name == driver {
			return true
		}
	}
	return false
}

// applyStagedPlacementToNewSandbox pins a cold-created Sandbox to the same
// provider domain as its staged volumes. Prewarmed Sandboxes are checked after
// scheduling by candidateSupportsStagedMounts; a newly created Sandbox has no
// node yet, so the requirement must become a scheduling constraint first.
func applyStagedPlacementToNewSandbox(sbx *agentsv1alpha1.Sandbox, opts *config.CSIMountOptions) error {
	if opts == nil || len(opts.StagedMountOptionList) == 0 {
		return nil
	}
	if sbx.Spec.Template == nil {
		return fmt.Errorf("resolved pod template is required for staged CSI placement")
	}
	if sbx.Spec.Template.Spec.NodeSelector == nil {
		sbx.Spec.Template.Spec.NodeSelector = make(map[string]string)
	}
	for _, mount := range opts.StagedMountOptionList {
		requirement := mount.Plan.Placement
		if requirement.DomainKey == "" && requirement.DomainValue == "" {
			continue
		}
		if requirement.DomainKey == "" || requirement.DomainValue == "" {
			return fmt.Errorf("incomplete staged CSI placement for driver %q: domain key and value must be set together", mount.Plan.Driver)
		}
		if current, exists := sbx.Spec.Template.Spec.NodeSelector[requirement.DomainKey]; exists && current != requirement.DomainValue {
			return fmt.Errorf("staged CSI placement conflicts with node selector %s=%q: driver %q requires %q",
				requirement.DomainKey, current, mount.Plan.Driver, requirement.DomainValue)
		}
		sbx.Spec.Template.Spec.NodeSelector[requirement.DomainKey] = requirement.DomainValue
	}

	// NewSandboxFromSandboxSet resolves TemplateRef into a private template copy.
	// Keep that snapshot as the only source of truth so the controller cannot
	// re-resolve the reference and lose the per-claim placement constraint.
	sbx.Spec.TemplateRef = nil
	return nil
}

func processKubeletAnchorMounts(
	ctx context.Context,
	kubeClient client.Client,
	apiReader client.Reader,
	sbx *agentsv1alpha1.Sandbox,
	opts config.CSIMountOptions,
) (time.Duration, error) {
	start := time.Now()
	for i := range opts.StagedMountOptionList {
		if err := mountKubeletAnchor(ctx, kubeClient, apiReader, sbx, &opts.StagedMountOptionList[i].Plan); err != nil {
			return time.Since(start), err
		}
	}
	return time.Since(start), nil
}

func mountKubeletAnchor(
	ctx context.Context,
	kubeClient client.Client,
	apiReader client.Reader,
	sbx *agentsv1alpha1.Sandbox,
	plan *storages.MountPlan,
) error {
	if plan == nil || plan.Strategy != storages.MountStrategyKubeletAnchor || plan.Anchor == nil {
		return fmt.Errorf("invalid kubelet-anchor mount plan")
	}
	if err := candidateSupportsStagedMounts(ctx, apiReader, sbx, &config.CSIMountOptions{
		StagedMountOptionList: []config.StagedMountConfig{{Plan: *plan}},
	}); err != nil {
		return fmt.Errorf("sandbox placement does not satisfy staged mount: %w", err)
	}
	pod, err := buildKubeletAnchorPod(ctx, apiReader, sbx, plan)
	if err != nil {
		return err
	}
	if err := kubeClient.Create(ctx, pod); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create kubelet anchor %s/%s: %w", pod.Namespace, pod.Name, err)
		}
		existing := &corev1.Pod{}
		if getErr := apiReader.Get(ctx, client.ObjectKeyFromObject(pod), existing); getErr != nil {
			return fmt.Errorf("get existing kubelet anchor: %w", getErr)
		}
		if err := validateExistingAnchor(existing, pod); err != nil {
			return err
		}
	}

	waitCtx, cancel := context.WithTimeout(ctx, defaultKubeletAnchorTimeout)
	defer cancel()
	if err := waitForKubeletAnchorReady(waitCtx, apiReader, client.ObjectKeyFromObject(pod)); err != nil {
		return err
	}
	if err := runtimeclient.ExposeKubeletAnchor(ctx, sbx, plan.VolumeIdentity, plan.Anchor.TargetPath); err != nil {
		cleanupErr := deleteKubeletAnchor(ctx, kubeClient, apiReader, sbx, pod.Namespace, pod.Name)
		return errors.Join(err, cleanupErr)
	}
	return nil
}

func buildKubeletAnchorPod(
	ctx context.Context,
	reader client.Reader,
	sbx *agentsv1alpha1.Sandbox,
	plan *storages.MountPlan,
) (*corev1.Pod, error) {
	anchor := plan.Anchor
	image := strings.TrimSpace(os.Getenv(KubeletAnchorImageEnv))
	if image == "" {
		return nil, fmt.Errorf("%s is required for staged CSI mounts", KubeletAnchorImageEnv)
	}
	if sbx.Status.PodInfo.PodUID == "" || strings.TrimSpace(sbx.Status.PodInfo.NodeName) == "" {
		return nil, fmt.Errorf("sandbox pod UID and node name are required for a kubelet anchor")
	}
	pvc := &corev1.PersistentVolumeClaim{}
	pvcKey := client.ObjectKey{Namespace: anchor.PersistentVolumeClaimNamespace, Name: anchor.PersistentVolumeClaimName}
	if err := reader.Get(ctx, pvcKey, pvc); err != nil {
		return nil, fmt.Errorf("get anchor PVC %s: %w", pvcKey, err)
	}
	if anchor.PersistentVolumeClaimUID != "" && pvc.UID != anchor.PersistentVolumeClaimUID {
		return nil, fmt.Errorf("anchor PVC %s UID changed from %s to %s", pvcKey, anchor.PersistentVolumeClaimUID, pvc.UID)
	}
	if pvc.Spec.VolumeName != anchor.PersistentVolumeName {
		return nil, fmt.Errorf("anchor PVC %s is bound to PV %q, expected %q", pvcKey, pvc.Spec.VolumeName, anchor.PersistentVolumeName)
	}

	identity := plan.VolumeIdentity
	if len(identity) < 20 {
		return nil, fmt.Errorf("kubelet anchor identity is too short")
	}
	resourceIdentity := sha256.Sum256([]byte(string(sbx.UID) + ":" + identity))
	podName := fmt.Sprintf("csi-anchor-%x", resourceIdentity[:10])
	hostTarget := path.Join(kubeletPodsRoot, string(sbx.Status.PodInfo.PodUID),
		"volumes/kubernetes.io~empty-dir/mount-root")
	containerTarget := path.Join(anchorTargetRoot, "anchor", identity)
	propagation := corev1.MountPropagationBidirectional
	args := []string{"--mode=run", "--source=" + anchorSourcePath, "--target=" + containerTarget}
	if anchor.ReadOnly {
		args = append(args, "--read-only=true")
	}
	grace := int64(40)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: anchor.PersistentVolumeClaimNamespace,
			Name:      podName,
			Labels: map[string]string{
				KubeletAnchorManagedLabel:     "true",
				KubeletAnchorSandboxNameLabel: sbx.Name,
				KubeletAnchorSandboxNSLabel:   sbx.Namespace,
			},
			Annotations: map[string]string{
				KubeletAnchorIdentityAnnotation: identity,
				KubeletAnchorTargetAnnotation:   anchor.TargetPath,
				KubeletAnchorSandboxUIDAnno:     string(sbx.UID),
				KubeletAnchorSandboxPodUIDAnno:  string(sbx.Status.PodInfo.PodUID),
				KubeletAnchorPVAnnotation:       anchor.PersistentVolumeName,
			},
			Finalizers: []string{KubeletAnchorFinalizer},
		},
		Spec: corev1.PodSpec{
			NodeName:                      sbx.Status.PodInfo.NodeName,
			RestartPolicy:                 corev1.RestartPolicyNever,
			TerminationGracePeriodSeconds: &grace,
			Containers: []corev1.Container{{
				Name:            KubeletAnchorContainerName,
				Image:           image,
				ImagePullPolicy: corev1.PullIfNotPresent,
				Args:            args,
				SecurityContext: &corev1.SecurityContext{Privileged: ptr.To(true)},
				VolumeMounts: []corev1.VolumeMount{
					{Name: "source", MountPath: anchorSourcePath, SubPath: anchor.SubPath, ReadOnly: anchor.ReadOnly},
					{Name: "target", MountPath: anchorTargetRoot, MountPropagation: &propagation},
				},
				ReadinessProbe: &corev1.Probe{
					ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{
						"/kubelet-anchor-mounter", "--mode=check", "--target=" + containerTarget,
					}}},
					PeriodSeconds: 1,
				},
			}},
			Volumes: []corev1.Volume{
				{Name: "source", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: anchor.PersistentVolumeClaimName,
					ReadOnly:  anchor.ReadOnly,
				}}},
				{Name: "target", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{
					Path: hostTarget,
					Type: ptr.To(corev1.HostPathDirectory),
				}}},
			},
		},
	}
	if pullSecret := strings.TrimSpace(os.Getenv(KubeletAnchorPullSecretEnv)); pullSecret != "" {
		pod.Spec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: pullSecret}}
	}
	return pod, nil
}

func validateExistingAnchor(existing, desired *corev1.Pod) error {
	if existing.DeletionTimestamp != nil {
		return fmt.Errorf("kubelet anchor %s/%s is terminating", existing.Namespace, existing.Name)
	}
	for _, key := range []string{
		KubeletAnchorIdentityAnnotation,
		KubeletAnchorSandboxUIDAnno,
		KubeletAnchorSandboxPodUIDAnno,
		KubeletAnchorTargetAnnotation,
	} {
		if existing.Annotations[key] != desired.Annotations[key] {
			return fmt.Errorf("existing kubelet anchor %s/%s has conflicting %s", existing.Namespace, existing.Name, key)
		}
	}
	if existing.Spec.NodeName != desired.Spec.NodeName {
		return fmt.Errorf("existing kubelet anchor is pinned to node %s, expected %s", existing.Spec.NodeName, desired.Spec.NodeName)
	}
	return nil
}

func waitForKubeletAnchorReady(ctx context.Context, reader client.Reader, key client.ObjectKey) error {
	return wait.PollUntilContextCancel(ctx, 500*time.Millisecond, true, func(ctx context.Context) (bool, error) {
		pod := &corev1.Pod{}
		if err := reader.Get(ctx, key, pod); err != nil {
			return false, err
		}
		if pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodSucceeded {
			return false, fmt.Errorf("kubelet anchor %s terminated in phase %s", key, pod.Status.Phase)
		}
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
				return true, nil
			}
		}
		return false, nil
	})
}

func cleanupKubeletAnchors(
	ctx context.Context,
	kubeClient client.Client,
	apiReader client.Reader,
	sbx *agentsv1alpha1.Sandbox,
) error {
	pods := &corev1.PodList{}
	if err := apiReader.List(ctx, pods, client.MatchingLabels{
		KubeletAnchorManagedLabel:     "true",
		KubeletAnchorSandboxNameLabel: sbx.Name,
		KubeletAnchorSandboxNSLabel:   sbx.Namespace,
	}); err != nil {
		return fmt.Errorf("list kubelet anchors: %w", err)
	}
	sortKubeletAnchorsDeepestFirst(pods.Items)
	var errs []error
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.Annotations[KubeletAnchorSandboxUIDAnno] != string(sbx.UID) {
			continue
		}
		if err := deleteKubeletAnchor(ctx, kubeClient, apiReader, sbx, pod.Namespace, pod.Name); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func sortKubeletAnchorsDeepestFirst(pods []corev1.Pod) {
	sort.SliceStable(pods, func(i, j int) bool {
		left := path.Clean(pods[i].Annotations[KubeletAnchorTargetAnnotation])
		right := path.Clean(pods[j].Annotations[KubeletAnchorTargetAnnotation])
		return strings.Count(left, "/") > strings.Count(right, "/")
	})
}

func deleteKubeletAnchor(
	ctx context.Context,
	kubeClient client.Client,
	apiReader client.Reader,
	sbx *agentsv1alpha1.Sandbox,
	namespace, name string,
) error {
	key := client.ObjectKey{Namespace: namespace, Name: name}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name}}
	if err := kubeClient.Delete(ctx, pod); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete kubelet anchor %s: %w", key, err)
	}
	waitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), defaultKubeletAnchorTimeout)
	defer cancel()
	return wait.PollUntilContextCancel(waitCtx, 500*time.Millisecond, true, func(ctx context.Context) (bool, error) {
		current := &corev1.Pod{}
		if err := apiReader.Get(ctx, key, current); err != nil {
			if apierrors.IsNotFound(err) {
				return true, nil
			}
			return false, err
		}
		terminated := anchorContainerTermination(current)
		if terminated == nil {
			return false, nil
		}
		if terminated.ExitCode != 0 {
			return false, fmt.Errorf("kubelet anchor unmount failed with exit code %d: %s", terminated.ExitCode, terminated.Message)
		}
		identity := current.Annotations[KubeletAnchorIdentityAnnotation]
		targetPath := current.Annotations[KubeletAnchorTargetAnnotation]
		if sbx != nil && identity != "" && targetPath != "" &&
			string(sbx.Status.PodInfo.PodUID) == current.Annotations[KubeletAnchorSandboxPodUIDAnno] {
			if err := runtimeclient.UnexposeKubeletAnchor(ctx, sbx, identity, targetPath); err != nil {
				return false, err
			}
		}
		base := current.DeepCopy()
		current.Finalizers = removeString(current.Finalizers, KubeletAnchorFinalizer)
		if err := kubeClient.Patch(ctx, current, client.MergeFrom(base)); err != nil && !apierrors.IsNotFound(err) {
			return false, err
		}
		return false, nil
	})
}

func anchorContainerTermination(pod *corev1.Pod) *corev1.ContainerStateTerminated {
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name == KubeletAnchorContainerName {
			return status.State.Terminated
		}
	}
	return nil
}

func removeString(values []string, target string) []string {
	result := values[:0]
	for _, value := range values {
		if value != target {
			result = append(result, value)
		}
	}
	return result
}

// runKubeletAnchorReconciler repairs finalizer transitions that were interrupted
// by a manager restart and reaps anchors whose Sandbox, claim, or Pod incarnation
// no longer exists. The Anchor Pod itself is the persistent lifecycle record.
func (i *Infra) runKubeletAnchorReconciler(ctx context.Context) {
	ticker := time.NewTicker(kubeletAnchorReconcilePeriod)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := i.reconcileKubeletAnchors(ctx); err != nil {
				klog.FromContext(ctx).Error(err, "failed to reconcile kubelet anchors")
			}
		}
	}
}

func (i *Infra) reconcileKubeletAnchors(ctx context.Context) error {
	pods := &corev1.PodList{}
	if err := i.Cache.GetAPIReader().List(ctx, pods, client.MatchingLabels{KubeletAnchorManagedLabel: "true"}); err != nil {
		return fmt.Errorf("list kubelet anchors for reconciliation: %w", err)
	}
	sortKubeletAnchorsDeepestFirst(pods.Items)
	var errs []error
	for idx := range pods.Items {
		pod := &pods.Items[idx]
		sbx := &agentsv1alpha1.Sandbox{}
		key := client.ObjectKey{
			Namespace: pod.Labels[KubeletAnchorSandboxNSLabel],
			Name:      pod.Labels[KubeletAnchorSandboxNameLabel],
		}
		getErr := i.Cache.GetAPIReader().Get(ctx, key, sbx)
		validSandbox := getErr == nil && string(sbx.UID) == pod.Annotations[KubeletAnchorSandboxUIDAnno]
		if getErr != nil && !apierrors.IsNotFound(getErr) {
			errs = append(errs, fmt.Errorf("get sandbox %s for anchor %s/%s: %w", key, pod.Namespace, pod.Name, getErr))
			continue
		}
		claimed := validSandbox && sbx.Labels[agentsv1alpha1.LabelSandboxIsClaimed] == agentsv1alpha1.True
		currentPod := validSandbox && string(sbx.Status.PodInfo.PodUID) == pod.Annotations[KubeletAnchorSandboxPodUIDAnno]
		if pod.DeletionTimestamp == nil && claimed && currentPod {
			continue
		}
		var cleanupSandbox *agentsv1alpha1.Sandbox
		if validSandbox && currentPod {
			cleanupSandbox = sbx
		}
		if err := deleteKubeletAnchor(ctx, i.Cache.GetClient(), i.Cache.GetAPIReader(), cleanupSandbox, pod.Namespace, pod.Name); err != nil {
			errs = append(errs, fmt.Errorf("reconcile kubelet anchor %s/%s: %w", pod.Namespace, pod.Name, err))
		}
	}
	return errors.Join(errs...)
}

// CleanupDynamicMounts is the optional infrastructure hook used by the public
// delete/recycle path. It blocks reuse until every anchor reports a successful
// unmount and the sandbox-visible symlink is removed.
func (i *Infra) CleanupDynamicMounts(ctx context.Context, sandbox managerinfra.Sandbox) error {
	sbx := &agentsv1alpha1.Sandbox{}
	key := client.ObjectKey{Namespace: sandbox.GetNamespace(), Name: sandbox.GetName()}
	if err := i.Cache.GetClient().Get(ctx, key, sbx); err != nil {
		return client.IgnoreNotFound(err)
	}
	return cleanupKubeletAnchors(ctx, i.Cache.GetClient(), i.Cache.GetAPIReader(), sbx)
}
