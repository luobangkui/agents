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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestValidateOptionsRejectsEscapes(t *testing.T) {
	tests := []struct {
		name    string
		opts    options
		wantErr string
	}{
		{name: "valid", opts: options{mode: "run", source: "/source", target: "/target/anchor/abc"}},
		{name: "target escape", opts: options{mode: "run", source: "/source", target: "/target/anchor/../escape"}, wantErr: "target"},
		{name: "source escape", opts: options{mode: "run", source: "/etc", target: "/target/anchor/abc"}, wantErr: "source"},
		{name: "unsupported mode", opts: options{mode: "other", target: "/target/anchor/abc"}, wantErr: "unsupported"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateOptions(tt.opts)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestMountMatchesFromReader(t *testing.T) {
	mountInfo := strings.Join([]string{
		"40 31 0:37 /volume /source rw,relatime - gpfs gpfs rw",
		"41 31 0:37 /volume /target/anchor/abc rw,relatime - gpfs gpfs rw",
		"42 31 0:38 /other /target/anchor/foreign ro,relatime - ext4 /dev/sda ro",
	}, "\n")
	tests := []struct {
		name     string
		target   string
		readOnly bool
		want     bool
	}{
		{name: "same source and mode", target: "/target/anchor/abc", want: true},
		{name: "mode mismatch", target: "/target/anchor/abc", readOnly: true},
		{name: "source mismatch", target: "/target/anchor/foreign", readOnly: true},
		{name: "missing target", target: "/target/anchor/missing"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matched, err := mountMatchesFromReader(strings.NewReader(mountInfo), "/source", tt.target, tt.readOnly)
			require.NoError(t, err)
			require.Equal(t, tt.want, matched)
		})
	}
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

func TestUnmountWithRetryRemovesStackedMounts(t *testing.T) {
	originalMounted, originalUnmount := mountedFn, unmountFn
	t.Cleanup(func() { mountedFn, unmountFn = originalMounted, originalUnmount })

	remaining := 2
	mountedFn = func(string) (bool, error) { return remaining > 0, nil }
	unmountFn = func(string) error {
		remaining--
		return nil
	}
	require.NoError(t, unmountWithRetry("/target/anchor/abc", time.Now().Add(time.Second)))
	require.Zero(t, remaining)
}
