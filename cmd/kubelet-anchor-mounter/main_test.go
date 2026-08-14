package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestValidateOptionsRejectsEscapes(t *testing.T) {
	require.NoError(t, validateOptions(options{mode: "run", source: "/source", target: "/target/anchor/abc"}))
	require.ErrorContains(t, validateOptions(options{mode: "run", source: "/source", target: "/target/anchor/../escape"}), "target")
	require.ErrorContains(t, validateOptions(options{mode: "run", source: "/etc", target: "/target/anchor/abc"}), "source")
	require.ErrorContains(t, validateOptions(options{mode: "other", target: "/target/anchor/abc"}), "unsupported")
}

func TestIsMountPointFromReader(t *testing.T) {
	mountInfo := "41 31 0:37 / /target/anchor/abc rw,relatime - gpfs gpfs rw\n"
	mounted, err := isMountPointFromReader(strings.NewReader(mountInfo), "/target/anchor/abc")
	require.NoError(t, err)
	require.True(t, mounted)

	mounted, err = isMountPointFromReader(strings.NewReader(mountInfo), "/target/anchor/other")
	require.NoError(t, err)
	require.False(t, mounted)
}

func TestUnmountWithRetryIsIdempotent(t *testing.T) {
	originalMounted, originalUnmount := mountedFn, unmountFn
	t.Cleanup(func() { mountedFn, unmountFn = originalMounted, originalUnmount })

	mountedFn = func(string) (bool, error) { return false, nil }
	unmountFn = func(string) error { return errors.New("must not be called") }
	require.NoError(t, unmountWithRetry("/target/anchor/abc", time.Now()))
}
