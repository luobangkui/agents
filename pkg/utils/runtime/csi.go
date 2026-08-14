/*
Copyright 2025.

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

package runtime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/protoadapt"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	agentsv1alpha1 "github.com/openkruise/agents/api/v1alpha1"
	"github.com/openkruise/agents/pkg/agent-runtime/storages"
	"github.com/openkruise/agents/pkg/cache"
	"github.com/openkruise/agents/pkg/utils"
	csimountutils "github.com/openkruise/agents/pkg/utils/csiutils"
	"github.com/openkruise/agents/pkg/utils/logs"
	"github.com/openkruise/agents/pkg/utils/runtime/config"
	"github.com/openkruise/agents/proto/envd/process"
)

var MountCommand = "/mnt/envd/sandbox-runtime-storage"

// csiMountTimeout is process-wide because claim, clone, and resume paths share
// the same runtime command and must use one operational timeout.
var csiMountTimeout = config.DefaultCSIMountTimeout

func init() {
	flag.DurationVar(&csiMountTimeout, "csi-mount-timeout", config.DefaultCSIMountTimeout,
		"Timeout for a single dynamic CSI mount operation.")
}

// CSIMount creates a dynamic mount point in Sandbox with `sandbox-storage` cli.
// It accepts the raw Sandbox API object to avoid circular dependency on the sandboxcr package.
//
// NOTE: `sandbox-storage` cli should be injected with `sandbox-runtime` and will be replaced by a built-in service of
// `sandbox-runtime`.
func CSIMount(ctx context.Context, sbx *agentsv1alpha1.Sandbox, driver string, request string) error {
	return csiMount(ctx, sbx, driver, request, csiMountTimeout)
}

func csiMount(
	ctx context.Context,
	sbx *agentsv1alpha1.Sandbox,
	driver string,
	request string,
	timeout time.Duration,
) error {
	log := klog.FromContext(ctx).WithValues("sandbox", klog.KObj(sbx))
	startTime := time.Now()
	if timeout <= 0 {
		timeout = config.DefaultCSIMountTimeout
	}
	processConfig := &process.ProcessConfig{
		Cmd: MountCommand,
		Args: []string{
			"mount",
			"--driver", driver,
			"--config", request,
			"--timeout", timeout.String(),
		},
		Cwd: nil,
		Envs: map[string]string{
			"POD_UID": string(sbx.Status.PodInfo.PodUID),
		},
	}

	result, err := RunCommandWithRuntime(ctx, RunCmdFuncArgs{
		Sbx:           sbx,
		ProcessConfig: processConfig,
		Timeout:       timeout,
		// The mount CLI manipulates mounts inside the sandbox and must run as root.
		AuthUser: "root",
	})
	if err != nil {
		log.Error(err, "failed to run command", "stdout", result.Stdout, "stderr", result.Stderr)
		return err
	}
	if result.ExitCode != 0 {
		err = fmt.Errorf("command failed: [%d] %s", result.ExitCode, result.Stderr)
		log.Error(err, "command failed", "exitCode", result.ExitCode)
		return err
	}
	log.Info("execute csi mount command", "driverName", driver, "mountCost", time.Since(startTime))
	return nil
}

// CSIUnmount releases a dynamic mount through the legacy sandbox-storage CLI.
// It accepts the original NodePublishVolume request because that is the stable
// identity contract shared with the mount command.
func CSIUnmount(ctx context.Context, sbx *agentsv1alpha1.Sandbox, driver string, request string) error {
	return csiUnmount(ctx, sbx, driver, request, csiMountTimeout)
}

func csiUnmount(
	ctx context.Context,
	sbx *agentsv1alpha1.Sandbox,
	driver string,
	request string,
	timeout time.Duration,
) error {
	log := klog.FromContext(ctx).WithValues("sandbox", klog.KObj(sbx))
	startTime := time.Now()
	if timeout <= 0 {
		timeout = config.DefaultCSIMountTimeout
	}
	processConfig := &process.ProcessConfig{
		Cmd: MountCommand,
		Args: []string{
			"unmount",
			"--driver", driver,
			"--config", request,
			"--timeout", timeout.String(),
		},
		Envs: map[string]string{
			"POD_UID": string(sbx.Status.PodInfo.PodUID),
		},
	}

	result, err := RunCommandWithRuntime(ctx, RunCmdFuncArgs{
		Sbx:           sbx,
		ProcessConfig: processConfig,
		Timeout:       timeout,
		AuthUser:      "root",
	})
	if err != nil {
		log.Error(err, "failed to run CSI unmount command", "stdout", result.Stdout, "stderr", result.Stderr)
		return err
	}
	if result.ExitCode != 0 {
		err = fmt.Errorf("command failed: [%d] %s", result.ExitCode, result.Stderr)
		log.Error(err, "CSI unmount command failed", "exitCode", result.ExitCode)
		return err
	}
	log.Info("execute csi unmount command", "driverName", driver, "unmountCost", time.Since(startTime))
	return nil
}

// ExposeKubeletAnchor creates the user-visible symlink after a same-node
// kubelet anchor has propagated its bind mount into the sandbox mount-root.
func ExposeKubeletAnchor(ctx context.Context, sbx *agentsv1alpha1.Sandbox, identity, mountPath string) error {
	return changeKubeletAnchorExposure(ctx, sbx, "expose", identity, mountPath)
}

// UnexposeKubeletAnchor removes only the symlink owned by identity. The anchor
// mounter must already have unmounted the propagated bind before this is called.
func UnexposeKubeletAnchor(ctx context.Context, sbx *agentsv1alpha1.Sandbox, identity, mountPath string) error {
	return changeKubeletAnchorExposure(ctx, sbx, "unexpose", identity, mountPath)
}

func changeKubeletAnchorExposure(ctx context.Context, sbx *agentsv1alpha1.Sandbox, operation, identity, mountPath string) error {
	result, err := RunCommandWithRuntime(ctx, RunCmdFuncArgs{
		Sbx: sbx,
		ProcessConfig: &process.ProcessConfig{
			Cmd: MountCommand,
			Args: []string{
				operation,
				"--identity", identity,
				"--mount-path", mountPath,
			},
		},
		Timeout:  csiMountTimeout,
		AuthUser: "root",
	})
	if err != nil {
		return fmt.Errorf("failed to %s kubelet anchor: %w (stderr: %s)", operation, err, result.Stderr)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("failed to %s kubelet anchor: exit code %d: %s", operation, result.ExitCode, result.Stderr)
	}
	return nil
}

// ProcessCSIMounts performs CSI volume mounting operations for all mount configurations concurrently.
// It uses opts.Concurrency to limit the number of concurrent mount goroutines.
// If Concurrency is 0 or negative, it defaults to config.DefaultCSIMountConcurrency.
// Returns the total duration spent on all mount operations and all encountered errors (joined via errors.Join).
//
// rtOpts selects the mount transport per sandbox: when non-empty (typically the
// TLS options resolved by TransportOptionsFor for a sandbox advertising
// AnnotationRuntimeTLSPort), every mount goes through the runtime storage API
// (POST /v1/storage/mounts over HTTPS with forced resolution). When empty, the
// legacy sandbox-storage CLI path over the envd process protocol is used, so
// pre-TLS sandboxes keep their existing behavior untouched.
func ProcessCSIMounts(ctx context.Context, sbx *agentsv1alpha1.Sandbox, opts config.CSIMountOptions, rtOpts ...Option) (time.Duration, error) {
	log := klog.FromContext(ctx).WithValues("sandbox", klog.KObj(sbx))
	start := time.Now()

	var wg sync.WaitGroup
	errCh := make(chan error, len(opts.MountOptionList))

	// Use a semaphore channel to limit concurrency
	concurrency := opts.Concurrency
	if concurrency <= 0 {
		concurrency = config.DefaultCSIMountConcurrency
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = csiMountTimeout
	}
	sem := make(chan struct{}, concurrency)

	for i, opt := range opts.MountOptionList {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, opt config.MountConfig) {
			defer wg.Done()
			defer func() { <-sem }()
			// Never log the MountConfig itself: PublishRequest carries the volume
			// Secrets and PublishContext (see MountConfig.PublishRequest). The driver
			// plus the position in the list is enough to correlate an entry with
			// its per-mount logs, which add the target path.
			mountDuration, err := doCSIMountWithTimeout(ctx, sbx, opt, timeout, rtOpts...)
			if err != nil {
				log.Error(err, "failed to perform CSI mount", "driver", opt.Driver, "mountIndex", i)
				errCh <- err
				return
			}
			log.Info("CSI mount completed successfully",
				"driver", opt.Driver,
				"mountIndex", i,
				"duration", mountDuration)
		}(i, opt)
	}

	wg.Wait()
	close(errCh)

	var errs []error
	for err := range errCh {
		errs = append(errs, err)
	}
	return time.Since(start), errors.Join(errs...)
}

// ProcessCSIUnmounts releases all resolved mounts. Targets are processed
// deepest-first and sequentially so a parent path cannot be detached while a
// nested child mount is still active. All failures are collected to give later
// targets a chance to clean up.
func ProcessCSIUnmounts(ctx context.Context, sbx *agentsv1alpha1.Sandbox, opts config.CSIMountOptions, rtOpts ...Option) (time.Duration, error) {
	start := time.Now()
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = csiMountTimeout
	}
	unmounts := append([]config.MountConfig(nil), opts.MountOptionList...)
	sort.SliceStable(unmounts, func(i, j int) bool {
		return mountPathDepth(unmounts[i].PublishRequest) > mountPathDepth(unmounts[j].PublishRequest)
	})

	var errs []error
	for _, opt := range unmounts {
		_, err := doCSIUnmountWithTimeout(ctx, sbx, opt, timeout, rtOpts...)
		if err != nil {
			errs = append(errs, err)
		}
	}
	return time.Since(start), errors.Join(errs...)
}

func mountPathDepth(req *csi.NodePublishVolumeRequest) int {
	if req == nil {
		return -1
	}
	return strings.Count(path.Clean(req.TargetPath), "/")
}

// doCSIMount performs a single CSI mount, dispatching between the two coexisting
// transports: the runtime storage API (HTTPS, when rtOpts carries the TLS options
// for a TLS-capable sandbox) and the legacy sandbox-storage CLI (plaintext envd
// process protocol) for everything else. The dispatch key is intentionally just
// "rtOpts non-empty": TransportOptionsFor is the single oracle deciding whether a
// sandbox is served over TLS, so this function stays free of annotation parsing.
func doCSIMount(ctx context.Context, sbx *agentsv1alpha1.Sandbox, opts config.MountConfig, rtOpts ...Option) (time.Duration, error) {
	return doCSIMountWithTimeout(ctx, sbx, opts, csiMountTimeout, rtOpts...)
}

func doCSIMountWithTimeout(
	ctx context.Context,
	sbx *agentsv1alpha1.Sandbox,
	opts config.MountConfig,
	timeout time.Duration,
	rtOpts ...Option,
) (time.Duration, error) {
	ctx = logs.Extend(ctx, "action", "csiMount")
	if timeout <= 0 {
		timeout = config.DefaultCSIMountTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := time.Now()
	if len(rtOpts) > 0 {
		// New path: the runtime storage API carries the typed CSI message, so the
		// request travels as-is and protojson owns its encoding.
		_, err := NewRuntime(sbx, rtOpts...).Storage().Mount(ctx, CreateMountRequest{
			Driver:         opts.Driver,
			PublishRequest: opts.PublishRequest,
		})
		if err != nil {
			// The transport error names the endpoint, not the mount: add the
			// driver so both paths fail with the same amount of context.
			return time.Since(start), fmt.Errorf("failed to mount via runtime storage API for driver %q: %w", opts.Driver, err)
		}
		return time.Since(start), nil
	}
	// Legacy path: the CLI consumes an opaque blob, so encode the message here —
	// the last step before it becomes a command-line argument.
	requestRaw, err := encodePublishRequest(opts.PublishRequest)
	if err != nil {
		return time.Since(start), fmt.Errorf("failed to encode csi publish request for driver %q: %w", opts.Driver, err)
	}
	// Keep the mount and the elapsed time in separate statements: in a return
	// statement time.Since would be evaluated before the call it is meant to
	// measure, reporting a duration that excludes the mount itself.
	err = csiMount(ctx, sbx, opts.Driver, requestRaw, timeout)
	return time.Since(start), err
}

func doCSIUnmountWithTimeout(
	ctx context.Context,
	sbx *agentsv1alpha1.Sandbox,
	opts config.MountConfig,
	timeout time.Duration,
	rtOpts ...Option,
) (time.Duration, error) {
	ctx = logs.Extend(ctx, "action", "csiUnmount")
	if timeout <= 0 {
		timeout = config.DefaultCSIMountTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := time.Now()
	if opts.PublishRequest == nil {
		return time.Since(start), fmt.Errorf("csi publish request is required for unmount driver %q", opts.Driver)
	}
	if len(rtOpts) > 0 {
		storageAPI := NewRuntime(sbx, rtOpts...).Storage()
		unmountAPI, ok := storageAPI.(StorageUnmountAPI)
		if !ok {
			return time.Since(start), fmt.Errorf("runtime storage API does not support unmount")
		}
		_, err := unmountAPI.Unmount(ctx, DeleteMountRequest{
			Driver:         opts.Driver,
			PublishRequest: opts.PublishRequest,
		})
		if err != nil {
			return time.Since(start), fmt.Errorf("failed to unmount via runtime storage API for driver %q: %w", opts.Driver, err)
		}
		return time.Since(start), nil
	}
	requestRaw, err := encodePublishRequest(opts.PublishRequest)
	if err != nil {
		return time.Since(start), fmt.Errorf("failed to encode csi publish request for unmount driver %q: %w", opts.Driver, err)
	}
	err = csiUnmount(ctx, sbx, opts.Driver, requestRaw, timeout)
	return time.Since(start), err
}

// encodePublishRequest renders the typed CSI request as the base64 protobuf blob
// the sandbox-storage CLI expects on its --config flag. It is the only place the
// control plane still pre-encodes a mount request, and it disappears with the
// CLI transport.
func encodePublishRequest(publishRequest *csi.NodePublishVolumeRequest) (string, error) {
	// An empty --config would only fail inside the sandbox: reject it here, where
	// the error can still name the mount that is misconfigured.
	if publishRequest == nil {
		return "", fmt.Errorf("csi publish request is required")
	}
	data, err := proto.Marshal(protoadapt.MessageV2Of(publishRequest))
	if err != nil {
		return "", fmt.Errorf("failed to marshal csi publish request: %w", err)
	}
	return base64.StdEncoding.EncodeToString(data), nil
}

// GetCsiMountExtensionRequest parses the CSI mount extension request from object annotations.
func GetCsiMountExtensionRequest(s metav1.Object) ([]agentsv1alpha1.CSIMountConfig, error) {
	var csiMountRequests []agentsv1alpha1.CSIMountConfig
	csiMountRequestsRaw := s.GetAnnotations()[agentsv1alpha1.AnnotationCSIVolumeConfig]
	if csiMountRequestsRaw == "" {
		return nil, nil
	}
	if err := json.Unmarshal([]byte(csiMountRequestsRaw), &csiMountRequests); err != nil {
		return nil, fmt.Errorf("failed to unmarshal csi mount options: %v", err)
	}
	return csiMountRequests, nil
}

// ResolveCSIMountFromAnnotation parses CSI mount config from sandbox annotation and resolves it into MountOptionList.
// Returns nil if no CSI mount annotation is present.
func ResolveCSIMountFromAnnotation(ctx context.Context, obj metav1.Object, client client.Client, cache cache.Provider, storageRegistry storages.VolumeMountProviderRegistry) (*config.CSIMountOptions, error) {
	log := klog.FromContext(ctx)
	csiMountConfigs, err := GetCsiMountExtensionRequest(obj)
	if err != nil {
		log.Error(err, "failed to parse csi mount config from annotation")
		return nil, fmt.Errorf("failed to parse csi mount config from annotation: %w", err)
	}
	if len(csiMountConfigs) == 0 {
		return nil, nil
	}
	csiClient := csimountutils.NewCSIMountHandler(cache.GetClient(), cache.GetAPIReader(), storageRegistry, utils.DefaultSandboxDeployNamespace)
	opts := &config.CSIMountOptions{}
	for _, cfg := range csiMountConfigs {
		plan, genErr := csiClient.GenerateMountPlan(ctx, cfg)
		if genErr != nil {
			log.Error(genErr, "failed to generate csi mount options config", "mountConfig", cfg)
			return nil, fmt.Errorf("failed to generate csi mount options config: %w", genErr)
		}
		if genErr = opts.AppendMountPlan(plan); genErr != nil {
			return nil, genErr
		}
	}
	return opts, nil
}
