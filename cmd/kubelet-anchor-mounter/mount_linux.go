//go:build linux

package main

import (
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
		_ = unix.Unmount(target, 0)
		return fmt.Errorf("read-only remount: %w", err)
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
