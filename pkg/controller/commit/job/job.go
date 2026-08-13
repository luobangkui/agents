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

package job

import (
	"context"
	"strings"
	"time"

	"k8s.io/klog/v2"
)

// Executor defines the interface for running nerdctl commands.
type Executor func(ctx context.Context, opts ...CmdOpt) error

// defaultExecutor is the production executor.
var defaultExecutor Executor = NerdctlExec

// CommitOptions contains the explicit runtime inputs for a commit job.
type CommitOptions struct {
	ContainerID string
	Image       string
	// BaseImage is an optional OCI parent used to rebase NYDUS commits before push.
	BaseImage string
	// SourceImage is the running container image ref; used to infer BaseImage.
	SourceImage string
}

// DoCommit is the main entry point for the commit-job binary.
// It performs: setup registry auth → nerdctl commit → optional OCI rebase → nerdctl push.
func DoCommit(ctx context.Context, opts CommitOptions) int {
	return doCommitWith(ctx, opts, defaultExecutor)
}

func doCommitWith(ctx context.Context, opts CommitOptions, executor Executor) int {
	containerID := opts.ContainerID
	image := opts.Image

	if containerID == "" {
		klog.ErrorS(nil, "Commit container ID is empty", "arg", ArgContainerID)
		return ExitCodeCommitFailed
	}
	if image == "" {
		klog.ErrorS(nil, "Commit image is empty", "arg", ArgImage)
		return ExitCodeCommitFailed
	}

	klog.InfoS("Start commit", "containerID", containerID, "image", image,
		"baseImage", opts.BaseImage, "sourceImage", opts.SourceImage)

	// 1. Setup registry authentication
	if err := setupRegistryAuth(); err != nil {
		klog.ErrorS(err, "Failed to setup registry authentication, push may fail")
	}

	// Resolve source image early so NYDUS delivery-tag inference works even when
	// an older sandbox-controller did not pass --source-image.
	if strings.TrimSpace(opts.SourceImage) == "" {
		if src, err := inspectContainerImage(ctx, containerID); err != nil {
			klog.InfoS("Could not inspect container image for rebase inference", "err", err.Error())
		} else {
			opts.SourceImage = src
			klog.InfoS("Inferred source image from container", "sourceImage", src)
		}
	}

	// 2. nerdctl commit
	start := time.Now()
	if err := executor(ctx, WithArgs("commit", containerID, image)); err != nil {
		klog.ErrorS(err, "Commit failed", "containerID", containerID, "image", image)
		return ExitCodeCommitFailed
	}
	klog.InfoS("Commit succeeded", "elapsed", time.Since(start))

	// 3. Optional rebase onto OCI base (required when commit kept NYDUS parents)
	if code := maybeRebaseBeforePush(ctx, opts, executor); code != ExitCodeSuccess {
		return code
	}

	// 4. nerdctl push
	klog.InfoS("Start to push image", "image", image)
	start = time.Now()
	if err := executor(ctx, WithArgs("push", image)); err != nil {
		klog.ErrorS(err, "Push failed", "image", image)
		return ExitCodePushFailed
	}
	klog.InfoS("Push succeeded", "elapsed", time.Since(start))

	return ExitCodeSuccess
}

func maybeRebaseBeforePush(ctx context.Context, opts CommitOptions, executor Executor) int {
	needs, err := ImageHasNydusLayers(ctx, opts.Image)
	if err != nil {
		// Helper missing or inspect failed: only hard-fail when a base was requested.
		if ResolveBaseImage(opts.BaseImage, opts.SourceImage) == "" {
			klog.InfoS("Skip rebase check", "err", err.Error())
			return ExitCodeSuccess
		}
		klog.ErrorS(err, "Failed to inspect committed image for nydus layers")
		return ExitCodeCommitFailed
	}
	if !needs {
		klog.InfoS("Committed image has no nydus layers; skip rebase")
		return ExitCodeSuccess
	}

	base := ResolveBaseImage(opts.BaseImage, opts.SourceImage)
	if base == "" {
		klog.ErrorS(nil, "Committed image has nydus layers but no OCI baseImage/source delivery tag was provided")
		return ExitCodeCommitFailed
	}

	klog.InfoS("Pull OCI base for rebase", "base", base)
	if err := executor(ctx, WithArgs("pull", base)); err != nil {
		klog.ErrorS(err, "Pull base image failed", "base", base)
		return ExitCodeCommitFailed
	}
	klog.InfoS("Rebase committed image onto OCI base", "image", opts.Image, "base", base)
	if err := RebaseImageOntoBase(ctx, opts.Image, base); err != nil {
		klog.ErrorS(err, "Rebase failed", "image", opts.Image, "base", base)
		return ExitCodeCommitFailed
	}
	return ExitCodeSuccess
}
