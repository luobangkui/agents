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

package kubeletanchormounter

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"k8s.io/klog/v2"
)

const (
	anchorSourceRoot = "/source"
	anchorTargetRoot = "/target/anchor"
	unmountTimeout   = 30 * time.Second
	unmountRetry     = 500 * time.Millisecond
)

var (
	bindMountFn    = bindMount
	unmountFn      = unmount
	mountedFn      = isMountPoint
	mountMatchesFn = mountMatches
)

type options struct {
	mode     string
	source   string
	target   string
	readOnly bool
}

// Run parses one kubelet-anchor operation and owns its complete mount
// lifecycle. The cmd package only delegates process startup here.
func Run(args []string) error {
	fs := flag.NewFlagSet("kubelet-anchor-mounter", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	opts := options{}
	fs.StringVar(&opts.mode, "mode", "run", "run, check, or unmount")
	fs.StringVar(&opts.source, "source", anchorSourceRoot, "source path mounted by kubelet")
	fs.StringVar(&opts.target, "target", "", "target below /target/anchor")
	fs.BoolVar(&opts.readOnly, "read-only", false, "remount the bind read-only")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := validateOptions(opts); err != nil {
		return err
	}

	switch opts.mode {
	case "check":
		mounted, err := mountedFn(opts.target)
		if err != nil {
			return err
		}
		if !mounted {
			return fmt.Errorf("target %s is not a mount point", opts.target)
		}
		return nil
	case "unmount":
		return unmountWithRetry(opts.target, time.Now().Add(unmountTimeout))
	case "run":
		mounted, err := mountedFn(opts.target)
		if err != nil {
			return fmt.Errorf("inspect existing target %s: %w", opts.target, err)
		}
		if mounted {
			matches, matchErr := mountMatchesFn(opts.source, opts.target, opts.readOnly)
			if matchErr != nil {
				return fmt.Errorf("verify existing target %s: %w", opts.target, matchErr)
			}
			if !matches {
				return fmt.Errorf("target %s is already mounted from a different source or mode", opts.target)
			}
		} else if err := bindMountFn(opts.source, opts.target, opts.readOnly); err != nil {
			return fmt.Errorf("bind mount %s at %s: %w", opts.source, opts.target, err)
		}
		klog.InfoS("kubelet anchor mounted", "target", opts.target, "readOnly", opts.readOnly)
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
		defer signal.Stop(signals)
		<-signals
		if err := unmountWithRetry(opts.target, time.Now().Add(unmountTimeout)); err != nil {
			return err
		}
		klog.InfoS("kubelet anchor unmounted", "target", opts.target)
		return nil
	default:
		return fmt.Errorf("unsupported mode %q", opts.mode)
	}
}

func validateOptions(opts options) error {
	if opts.mode != "run" && opts.mode != "check" && opts.mode != "unmount" {
		return fmt.Errorf("unsupported mode %q", opts.mode)
	}
	cleanTarget := filepath.Clean(opts.target)
	if !filepath.IsAbs(cleanTarget) || cleanTarget == anchorTargetRoot ||
		!strings.HasPrefix(cleanTarget, anchorTargetRoot+string(os.PathSeparator)) {
		return fmt.Errorf("target must remain below %s", anchorTargetRoot)
	}
	if opts.mode == "run" {
		cleanSource := filepath.Clean(opts.source)
		if !filepath.IsAbs(cleanSource) || (cleanSource != anchorSourceRoot &&
			!strings.HasPrefix(cleanSource, anchorSourceRoot+string(os.PathSeparator))) {
			return fmt.Errorf("source must remain below %s", anchorSourceRoot)
		}
	}
	return nil
}

func unmountWithRetry(target string, deadline time.Time) error {
	var lastErr error
	for {
		mounted, err := mountedFn(target)
		if err == nil && !mounted {
			return nil
		}
		if err != nil {
			lastErr = err
		} else if err = unmountFn(target); err == nil {
			// Re-read mountinfo before reporting success. A repeated bind can
			// leave another layer at the same target even after Unmount returns
			// nil, and cleanup must remove every layer.
			continue
		} else {
			lastErr = err
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("failed to unmount %s before deadline: %w", target, lastErr)
		}
		time.Sleep(unmountRetry)
	}
}

type mountIdentity struct {
	device   string
	root     string
	readOnly bool
}

func mountMatchesFromReader(r io.Reader, source, target string, readOnly bool) (bool, error) {
	source = filepath.Clean(source)
	target = filepath.Clean(target)
	identities := make(map[string]mountIdentity, 2)
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 6 {
			continue
		}
		mountPoint := unescapeMountInfoField(fields[4])
		cleanMountPoint := filepath.Clean(mountPoint)
		if cleanMountPoint != source && cleanMountPoint != target {
			continue
		}
		identities[cleanMountPoint] = mountIdentity{
			device:   fields[2],
			root:     filepath.Clean(unescapeMountInfoField(fields[3])),
			readOnly: mountOptionsReadOnly(fields[5]),
		}
	}
	if err := scanner.Err(); err != nil {
		return false, err
	}
	sourceIdentity, sourceOK := identities[source]
	targetIdentity, targetOK := identities[target]
	return sourceOK && targetOK && sourceIdentity.device == targetIdentity.device &&
		sourceIdentity.root == targetIdentity.root && targetIdentity.readOnly == readOnly, nil
}

func mountOptionsReadOnly(raw string) bool {
	for _, option := range strings.Split(raw, ",") {
		if option == "ro" {
			return true
		}
	}
	return false
}

func unescapeMountInfoField(value string) string {
	return strings.NewReplacer(`\040`, " ", `\011`, "\t", `\134`, `\`).Replace(value)
}

func isMountPointFromReader(r io.Reader, target string) (bool, error) {
	target = filepath.Clean(target)
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 6 {
			continue
		}
		mountPoint := unescapeMountInfoField(fields[4])
		if filepath.Clean(mountPoint) == target {
			return true, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return false, err
	}
	return false, nil
}

func ignoreNotMounted(err error) error {
	if errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOENT) {
		return nil
	}
	return err
}
