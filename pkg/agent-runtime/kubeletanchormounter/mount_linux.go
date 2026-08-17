//go:build linux

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
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func bindMount(source, target string, readOnly bool) error {
	if err := os.MkdirAll(target, 0755); err != nil { // #nosec G301 -- mount directory
		return err
	}
	if err := unix.Mount(source, target, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
		return err
	}
	if !readOnly {
		return nil
	}
	if err := unix.Mount("", target, "", unix.MS_BIND|unix.MS_REMOUNT|unix.MS_RDONLY, ""); err != nil {
		rollbackErr := unix.Unmount(target, 0)
		return errors.Join(fmt.Errorf("read-only remount: %w", err),
			func() error {
				if rollbackErr == nil {
					return nil
				}
				return fmt.Errorf("rollback bind mount: %w", rollbackErr)
			}())
	}
	return nil
}

func unmount(target string) error {
	return ignoreNotMounted(unix.Unmount(target, 0))
}

func isMountPoint(target string) (bool, error) {
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return false, err
	}
	defer f.Close() // #nosec G104 -- read-only proc file
	return isMountPointFromReader(f, target)
}

func mountMatches(source, target string, readOnly bool) (bool, error) {
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return false, err
	}
	defer f.Close() // #nosec G104 -- read-only proc file
	return mountMatchesFromReader(f, source, target, readOnly)
}
