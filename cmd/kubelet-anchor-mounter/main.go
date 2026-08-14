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

package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	anchorSourceRoot = "/source"
	anchorTargetRoot = "/target/anchor"
	unmountTimeout   = 30 * time.Second
	unmountRetry     = 500 * time.Millisecond
)

var (
	bindMountFn = bindMount
	unmountFn   = unmount
	mountedFn   = isMountPoint
)

type options struct {
	mode     string
	source   string
	target   string
	readOnly bool
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Printf("kubelet-anchor-mounter failed: %v", err)
		os.Exit(1)
	}
}

func run(args []string) error {
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
		if err := bindMountFn(opts.source, opts.target, opts.readOnly); err != nil {
			return fmt.Errorf("bind mount %s at %s: %w", opts.source, opts.target, err)
		}
		log.Printf("kubelet anchor mounted target=%s readOnly=%t", opts.target, opts.readOnly)
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
		defer signal.Stop(signals)
		<-signals
		if err := unmountWithRetry(opts.target, time.Now().Add(unmountTimeout)); err != nil {
			return err
		}
		log.Printf("kubelet anchor unmounted target=%s", opts.target)
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
			return nil
		} else {
			lastErr = err
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("failed to unmount %s before deadline: %w", target, lastErr)
		}
		time.Sleep(unmountRetry)
	}
}

func isMountPointFromReader(r io.Reader, target string) (bool, error) {
	target = filepath.Clean(target)
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 6 {
			continue
		}
		mountPoint := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\134`, `\`).Replace(fields[4])
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
