//go:build !linux

package main

import "fmt"

func bindMount(string, string, bool) error {
	return fmt.Errorf("kubelet anchor mounts require Linux")
}

func unmount(string) error {
	return fmt.Errorf("kubelet anchor unmounts require Linux")
}

func isMountPoint(string) (bool, error) {
	return false, fmt.Errorf("mountinfo requires Linux")
}
