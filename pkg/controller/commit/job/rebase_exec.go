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
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"k8s.io/klog/v2"
)

// inspectContainerImage returns the image reference used to create the container.
// Uses nerdctl directly so older controllers that omit --source-image still work.
func inspectContainerImage(ctx context.Context, containerID string) (string, error) {
	bin, err := exec.LookPath("nerdctl")
	if err != nil {
		return "", err
	}
	args := []string{
		"--namespace=k8s.io",
		fmt.Sprintf("--host=%s", Config().ContainerdSock()),
		"inspect",
		"--format", "{{.Image}}",
		containerID,
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("nerdctl inspect: %s (%w)", strings.TrimSpace(stderr.String()), err)
	}
	img := strings.TrimSpace(stdout.String())
	if img == "" {
		return "", fmt.Errorf("empty image from nerdctl inspect")
	}
	return img, nil
}

// DefaultCommitRebaseBinary is the path of the sidecar rebase helper baked into
// the commit-job image.
const DefaultCommitRebaseBinary = "/commit-rebase"

// RebaseExec runs the commit-rebase helper.
var RebaseExec = runCommitRebase

func runCommitRebase(ctx context.Context, args ...string) (stdout string, exitCode int, err error) {
	bin := DefaultCommitRebaseBinary
	if v := strings.TrimSpace(os.Getenv("COMMIT_REBASE_BIN")); v != "" {
		bin = v
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	klog.InfoS("commit-rebase CMD", "args", append([]string{bin}, args...))
	runErr := cmd.Run()
	stdout = outBuf.String()
	if runErr == nil {
		return stdout, 0, nil
	}
	if ee, ok := runErr.(*exec.ExitError); ok {
		return stdout, ee.ExitCode(), fmt.Errorf("commit-rebase: %s (%w)", strings.TrimSpace(errBuf.String()), runErr)
	}
	return stdout, -1, fmt.Errorf("commit-rebase: %s (%w)", strings.TrimSpace(errBuf.String()), runErr)
}

// ImageHasNydusLayers returns true when the local image still references NYDUS layers.
func ImageHasNydusLayers(ctx context.Context, image string) (bool, error) {
	_, code, err := RebaseExec(ctx,
		"--sock="+Config().ContainerdSock(),
		"--namespace=k8s.io",
		"--image="+image,
		"--check-nydus",
	)
	switch code {
	case 0:
		return true, nil
	case 1:
		return false, nil
	default:
		return false, err
	}
}

// RebaseImageOntoBase rewrites image so parents come from baseImage.
func RebaseImageOntoBase(ctx context.Context, image, baseImage string) error {
	_, _, err := RebaseExec(ctx,
		"--sock="+Config().ContainerdSock(),
		"--namespace=k8s.io",
		"--image="+image,
		"--base="+baseImage,
	)
	return err
}
