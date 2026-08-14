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
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"os"
	"testing"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/golang/protobuf/proto"
	"github.com/openkruise/agents/pkg/agent-runtime/storage-cli/storage"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGetMD5String verifies that getMD5String produces the canonical
// 32-character lowercase hex md5 digest for representative inputs.
func TestGetMD5String(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "empty string",
			in:   "",
			want: "d41d8cd98f00b204e9800998ecf8427e",
		},
		{
			name: "ascii sample abc",
			in:   "abc",
			want: "900150983cd24fb0d6963f7d28e17f72",
		},
		{
			name: "typical mount path",
			in:   "/data",
			want: "4caa791091d21d23e63637080226f370",
		},
		{
			name: "nested path",
			in:   "/mnt/oss/cache",
			want: "65bba0d321e176da2a652285cc471aae",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getMD5String(tt.in)
			assert.Equal(t, tt.want, got)
			assert.Len(t, got, 32, "md5 hex string must be 32 chars")
		})
	}
}

// TestGetMD5String_Deterministic ensures the same input always yields the
// same digest within a single process — guarding against accidental use of
// a non-deterministic hash.
func TestGetMD5String_Deterministic(t *testing.T) {
	const input = "/var/lib/sandbox"
	first := getMD5String(input)
	for i := 0; i < 5; i++ {
		assert.Equal(t, first, getMD5String(input))
	}
}

func TestGetMountTargetHash(t *testing.T) {
	base := csi.NodePublishVolumeRequest{
		VolumeId:   "shared-oss-pv",
		TargetPath: "/bohr-workspace",
		VolumeContext: map[string]string{
			"bucket":                     "bohr-sandbox-test",
			"path":                       "/bohr-sandbox/users/1/session-a",
			"csi.storage.k8s.io/pod.uid": "pod-a",
		},
	}

	t.Run("stable across map order and pod identity", func(t *testing.T) {
		other := base
		other.VolumeContext = map[string]string{
			"csi.storage.k8s.io/pod.uid": "pod-b",
			"path":                       "/bohr-sandbox/users/1/session-a",
			"bucket":                     "bohr-sandbox-test",
		}
		assert.Equal(t, getMountTargetHash("oss.csi.example.com", base),
			getMountTargetHash("oss.csi.example.com", other))
	})

	t.Run("different session path uses different host mount", func(t *testing.T) {
		other := base
		other.VolumeContext = map[string]string{
			"bucket": "bohr-sandbox-test",
			"path":   "/bohr-sandbox/users/1/session-b",
		}
		assert.NotEqual(t, getMountTargetHash("oss.csi.example.com", base),
			getMountTargetHash("oss.csi.example.com", other))
	})

	t.Run("driver and volume are part of identity", func(t *testing.T) {
		other := base
		other.VolumeId = "another-pv"
		assert.NotEqual(t, getMountTargetHash("oss.csi.example.com", base),
			getMountTargetHash("oss.csi.example.com", other))
		assert.NotEqual(t, getMountTargetHash("oss.csi.example.com", base),
			getMountTargetHash("nas.csi.example.com", base))
	})
}

// TestValidateGeneralParams_ExpectError mirrors the existing wantErr-style
// test but uses the project-standard expectError string so error messages
// are also asserted.
func TestValidateGeneralParams_ExpectError(t *testing.T) {
	const podUIDKey = "csi.storage.k8s.io/pod.uid"

	tests := []struct {
		name        string
		volumeCtx   map[string]string
		expectError string
	}{
		{
			name:        "valid pod uid",
			volumeCtx:   map[string]string{podUIDKey: "pod-123"},
			expectError: "",
		},
		{
			name:        "empty pod uid",
			volumeCtx:   map[string]string{podUIDKey: ""},
			expectError: "Pod UID is required",
		},
		{
			name:        "whitespace-only pod uid",
			volumeCtx:   map[string]string{podUIDKey: "   \t  "},
			expectError: "Pod UID is required",
		},
		{
			name:        "missing key",
			volumeCtx:   map[string]string{},
			expectError: "Pod UID is required",
		},
		{
			name:        "nil volume context",
			volumeCtx:   nil,
			expectError: "Pod UID is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := newCSIRequestWithVolumeContext(tt.volumeCtx)
			err := validateGeneralParams(req)
			if tt.expectError == "" {
				assert.NoError(t, err)
				return
			}
			assert.Error(t, err)
			assert.Contains(t, err.Error(), tt.expectError)
		})
	}
}

// TestRootRun verifies the root command help handler is invoked without
// panic and emits non-empty help output.
func TestRootRun(t *testing.T) {
	cmd := newCommandForHelpCapture()
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetErr(out)

	assert.NotPanics(t, func() {
		rootRun(cmd, nil)
	})
	assert.NotEmpty(t, out.String(), "rootRun should emit help output")
}

// TestUnmountRun ensures the cobra handler delegates to the testable unmount
// implementation without terminating the process on success.
func TestUnmountRun(t *testing.T) {
	cmd := newCommandForHelpCapture()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	originalRunUnmount := runUnmountFn
	runUnmountFn = func(got *cobra.Command) error {
		assert.Same(t, cmd, got)
		return nil
	}
	t.Cleanup(func() { runUnmountFn = originalRunUnmount })

	assert.NotPanics(t, func() {
		unmountRun(cmd, nil)
	})
}

// TestVersionCmd_Run captures the version subcommand's stdout output and
// asserts the expected version banner is printed.
func TestVersionCmd_Run(t *testing.T) {
	origVersion := version
	version = "v1.2.3-test"
	t.Cleanup(func() { version = origVersion })

	out := captureStdout(t, func() {
		versionCmd.Run(versionCmd, nil)
	})
	assert.Contains(t, out, "sandbox-storage v1.2.3-test")
}

// TestCommandMetadata pins the public CLI surface (command names and short
// help) so accidental renames are caught by unit tests.
func TestCommandMetadata(t *testing.T) {
	tests := []struct {
		name      string
		cmd       *cobra.Command
		wantUse   string
		wantShort string
	}{
		{
			name:      "root command",
			cmd:       rootCmd,
			wantUse:   "sandbox-storage",
			wantShort: "A CLI tool for storage nas or oss mount/unmount for sandbox runtime",
		},
		{
			name:      "mount command",
			cmd:       mountCmd,
			wantUse:   "mount",
			wantShort: "Mount storage (NAS or OSS) to specified path",
		},
		{
			name:      "unmount command",
			cmd:       unmountCmd,
			wantUse:   "unmount",
			wantShort: "Unmount storage from specified path",
		},
		{
			name:      "version command",
			cmd:       versionCmd,
			wantUse:   "version",
			wantShort: "Print the version number of sandbox-storage",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.wantUse, tt.cmd.Use)
			assert.Equal(t, tt.wantShort, tt.cmd.Short)
			assert.NotNil(t, tt.cmd.Run, "Run handler must be wired")
		})
	}
}

// TestRootCmdPersistentFlags verifies the CLI-contract flags are wired onto
// the root command with the expected long and short names.
func TestRootCmdPersistentFlags(t *testing.T) {
	tests := []struct {
		name      string
		long      string
		shorthand string
	}{
		{name: "driver flag", long: "driver", shorthand: "d"},
		{name: "config flag", long: "config", shorthand: "c"},
		{name: "timeout flag", long: "timeout", shorthand: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := rootCmd.PersistentFlags().Lookup(tt.long)
			if assert.NotNil(t, f, "flag %q must be registered", tt.long) {
				assert.Equal(t, tt.shorthand, f.Shorthand)
			}
		})
	}
}

// TestRootCmdExecute_VersionSubcommand drives the cobra dispatcher end-to-end
// through the version subcommand and asserts the printed banner. This
// exercises the command-tree wiring that main() relies on without calling
// os.Exit.
func TestRootCmdExecute_VersionSubcommand(t *testing.T) {
	origVersion := version
	version = "v1.2.3-test"
	t.Cleanup(func() { version = origVersion })

	// Snapshot original state so the global rootCmd remains unmodified
	// for later tests.
	prevArgs := os.Args
	t.Cleanup(func() {
		os.Args = prevArgs
		rootCmd.SetArgs(nil)
	})

	// Add subcommands as main() does, but only if they are not already
	// attached to avoid cobra's "command already added" panic on reruns.
	ensureSubcommand(t, rootCmd, mountCmd)
	ensureSubcommand(t, rootCmd, unmountCmd)
	ensureSubcommand(t, rootCmd, versionCmd)

	rootCmd.SetArgs([]string{"version"})

	out := captureStdout(t, func() {
		assert.NoError(t, rootCmd.Execute())
	})
	assert.Contains(t, out, "sandbox-storage v1.2.3-test")
}

// --- test helpers -----------------------------------------------------------

// newCSIRequestWithVolumeContext builds a minimal NodePublishVolumeRequest
// with the provided volume context; used to drive validateGeneralParams.
func newCSIRequestWithVolumeContext(ctx map[string]string) csi.NodePublishVolumeRequest {
	return csi.NodePublishVolumeRequest{VolumeContext: ctx}
}

// captureStdout swaps os.Stdout for the duration of fn and returns the
// captured output. It also synchronizes the standard logger which writes to
// stderr by default but may be reconfigured by main().
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}

	origStdout := os.Stdout
	os.Stdout = w
	prevFlags := log.Flags()
	log.SetOutput(io.Discard)
	t.Cleanup(func() {
		os.Stdout = origStdout
		log.SetOutput(os.Stderr)
		log.SetFlags(prevFlags)
	})

	fn()
	_ = w.Close()

	buf := &bytes.Buffer{}
	if _, err := io.Copy(buf, r); err != nil {
		t.Fatalf("read captured stdout: %v", err)
	}
	return buf.String()
}

// newCommandForHelpCapture returns a throwaway cobra.Command suitable for
// capturing help output without mutating the global rootCmd.
func newCommandForHelpCapture() *cobra.Command {
	return &cobra.Command{
		Use:   "test-cmd",
		Short: "test command",
		Long:  "test command long description",
		Run:   func(*cobra.Command, []string) {},
	}
}

// ensureSubcommand attaches child to parent only if it has not been
// attached yet, which keeps the test idempotent across runs sharing the
// global rootCmd.
func ensureSubcommand(t *testing.T, parent, child *cobra.Command) {
	t.Helper()
	for _, c := range parent.Commands() {
		if c == child {
			return
		}
	}
	parent.AddCommand(child)
}

// --- fakes and helpers for TestRunMount ------------------------------------

// fakeProvider is a test-only Provider that records calls and delegates to
// optional function fields.
type fakeProvider struct {
	driverName string
	subDir     string
	validateFn func(csi.NodePublishVolumeRequest) error
	mountFn    func(context.Context, csi.NodePublishVolumeRequest) error
	unmountFn  func(context.Context, csi.NodePublishVolumeRequest) error
}

func (f *fakeProvider) Driver() string { return f.driverName }
func (f *fakeProvider) SubDir() string {
	if f.subDir != "" {
		return f.subDir
	}
	return "fake"
}
func (f *fakeProvider) Validate(req csi.NodePublishVolumeRequest) error {
	if f.validateFn != nil {
		return f.validateFn(req)
	}
	return nil
}
func (f *fakeProvider) Mount(ctx context.Context, req csi.NodePublishVolumeRequest, debug bool) error {
	if f.mountFn != nil {
		return f.mountFn(ctx, req)
	}
	return nil
}

func (f *fakeProvider) Unmount(ctx context.Context, req csi.NodePublishVolumeRequest) error {
	if f.unmountFn != nil {
		return f.unmountFn(ctx, req)
	}
	return nil
}

// makeBase64CSIConfig serialises req into a base64-encoded proto string
// suitable for the --config flag.
func makeBase64CSIConfig(t *testing.T, req *csi.NodePublishVolumeRequest) string {
	t.Helper()
	b, err := proto.Marshal(req)
	if err != nil {
		t.Fatalf("proto.Marshal: %v", err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

// withRunMountEnv saves all global CLI vars and injectable function variables,
// sets them to the provided values and registers a t.Cleanup to restore them.
func withRunMountEnv(t *testing.T, d, cfg, mn string) {
	t.Helper()
	origDriver, origConfig, origMountName := driver, config, mountName
	origMountTimeout := mountTimeout
	origMountFinderFn := mountFinderFn
	origStorageLookupFn := storageLookupFn
	origCreateSymlinkFn := createSymlinkFn
	origRemoveSymlinkFn := removeSymlinkFn
	origRemoveMountTargetFn := removeMountTargetFn
	driver, config, mountName = d, cfg, mn
	mountTimeout = storage.DefaultNodePublishVolumeTimeout
	t.Cleanup(func() {
		driver = origDriver
		config = origConfig
		mountName = origMountName
		mountTimeout = origMountTimeout
		mountFinderFn = origMountFinderFn
		storageLookupFn = origStorageLookupFn
		createSymlinkFn = origCreateSymlinkFn
		removeSymlinkFn = origRemoveSymlinkFn
		removeMountTargetFn = origRemoveMountTargetFn
	})
}

// silentCmd returns a cobra.Command that discards help / error output so that
// cmd.Help() calls inside runMount do not pollute test output.
func silentCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "test", Run: func(*cobra.Command, []string) {}}
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	return cmd
}

// TestRunMount exercises every control-flow branch of the runMount function
// by injecting fakes for the three external dependencies.
func TestRunMount(t *testing.T) {
	const (
		fakeDriver    = "fake.csi.example.com"
		fakeMountRoot = "/fake/mount-root"
		podUID        = "test-pod-uid-123"
		targetPath    = "/data/workspace"
	)

	validReq := &csi.NodePublishVolumeRequest{
		VolumeId:   "vol-001",
		TargetPath: targetPath,
		VolumeContext: map[string]string{
			"csi.storage.k8s.io/pod.uid": podUID,
		},
	}
	validConfig := makeBase64CSIConfig(t, validReq)

	// Reusable injectable functions.
	mountFinderOK := func(_ string, _ bool) (string, error) { return fakeMountRoot, nil }
	mountFinderFail := func(_ string, _ bool) (string, error) {
		return "", fmt.Errorf("mount-root not found in /proc/mounts")
	}
	lookupOK := func(_ string) (storage.Provider, bool) {
		return &fakeProvider{driverName: fakeDriver, subDir: "nas"}, true
	}
	lookupValidateErr := func(_ string) (storage.Provider, bool) {
		return &fakeProvider{
			driverName: fakeDriver,
			validateFn: func(_ csi.NodePublishVolumeRequest) error {
				return fmt.Errorf("validate: secret key missing")
			},
		}, true
	}
	lookupMountErr := func(_ string) (storage.Provider, bool) {
		return &fakeProvider{
			driverName: fakeDriver,
			mountFn: func(_ context.Context, _ csi.NodePublishVolumeRequest) error {
				return fmt.Errorf("mount: socket not found")
			},
		}, true
	}
	lookupMiss := func(_ string) (storage.Provider, bool) { return nil, false }
	symlinkOK := func(tgt, lnk string) error {
		assert.Contains(t, tgt, fakeMountRoot, "symlink target must be under mount root")
		assert.Equal(t, targetPath, lnk, "symlink link must be the original target path")
		return nil
	}
	symlinkFail := func(_, _ string) error { return fmt.Errorf("symlink: permission denied") }

	tests := []struct {
		name           string
		cfg            string
		drv            string
		mn             string
		mountFinderFn  func(string, bool) (string, error)
		storageLookupF func(string) (storage.Provider, bool)
		symlinkFn      func(string, string) error
		expectError    string
	}{
		{
			name:           "invalid base64 config",
			cfg:            "!!!not_valid_base64!!!",
			drv:            fakeDriver,
			mn:             "mount-root",
			mountFinderFn:  mountFinderOK,
			storageLookupF: lookupOK,
			symlinkFn:      symlinkOK,
			expectError:    "failed to decode CSI request config",
		},
		{
			name:           "valid base64 but invalid proto bytes",
			cfg:            base64.StdEncoding.EncodeToString([]byte{0xFF, 0xFE, 0x01}),
			drv:            fakeDriver,
			mn:             "mount-root",
			mountFinderFn:  mountFinderOK,
			storageLookupF: lookupOK,
			symlinkFn:      symlinkOK,
			expectError:    "failed to unmarshal CSI request",
		},
		{
			name: "missing pod uid, no env fallback",
			cfg: makeBase64CSIConfig(t, &csi.NodePublishVolumeRequest{
				TargetPath:    targetPath,
				VolumeContext: map[string]string{},
			}),
			drv:            fakeDriver,
			mn:             "mount-root",
			mountFinderFn:  mountFinderOK,
			storageLookupF: lookupOK,
			symlinkFn:      symlinkOK,
			expectError:    "Pod UID is required",
		},
		{
			name:           "mountfinder returns error",
			cfg:            validConfig,
			drv:            fakeDriver,
			mn:             "mount-root",
			mountFinderFn:  mountFinderFail,
			storageLookupF: lookupOK,
			symlinkFn:      symlinkOK,
			expectError:    "failed to find valid mount path",
		},
		{
			name:           "unsupported driver",
			cfg:            validConfig,
			drv:            "no-such-driver",
			mn:             "mount-root",
			mountFinderFn:  mountFinderOK,
			storageLookupF: lookupMiss,
			symlinkFn:      symlinkOK,
			expectError:    "unsupported storage driver",
		},
		{
			name:           "provider Validate fails",
			cfg:            validConfig,
			drv:            fakeDriver,
			mn:             "mount-root",
			mountFinderFn:  mountFinderOK,
			storageLookupF: lookupValidateErr,
			symlinkFn:      symlinkOK,
			expectError:    "validate: secret key missing",
		},
		{
			name:           "provider Mount fails",
			cfg:            validConfig,
			drv:            fakeDriver,
			mn:             "mount-root",
			mountFinderFn:  mountFinderOK,
			storageLookupF: lookupMountErr,
			symlinkFn:      symlinkOK,
			expectError:    "mount: socket not found",
		},
		{
			name:           "createSymlink fails",
			cfg:            validConfig,
			drv:            fakeDriver,
			mn:             "mount-root",
			mountFinderFn:  mountFinderOK,
			storageLookupF: lookupOK,
			symlinkFn:      symlinkFail,
			expectError:    "failed to create symlink",
		},
		{
			name:           "success",
			cfg:            validConfig,
			drv:            fakeDriver,
			mn:             "mount-root",
			mountFinderFn:  mountFinderOK,
			storageLookupF: lookupOK,
			symlinkFn:      symlinkOK,
			expectError:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withRunMountEnv(t, tt.drv, tt.cfg, tt.mn)
			log.SetOutput(io.Discard)
			t.Cleanup(func() { log.SetOutput(os.Stderr) })

			mountFinderFn = tt.mountFinderFn
			storageLookupFn = tt.storageLookupF
			createSymlinkFn = tt.symlinkFn

			err := runMount(silentCmd())
			if tt.expectError == "" {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectError)
			}
		})
	}
}

// TestRunMount_PodUIDFromEnv verifies the downward-API fallback: when the
// VolumeContext pod uid is empty the function reads POD_UID from the environment.
func TestRunMount_PodUIDFromEnv(t *testing.T) {
	t.Setenv("POD_UID", "env-pod-uid-999")

	req := &csi.NodePublishVolumeRequest{
		TargetPath:    "/data/workspace",
		VolumeContext: map[string]string{"csi.storage.k8s.io/pod.uid": ""},
	}
	withRunMountEnv(t, "fake.csi.example.com", makeBase64CSIConfig(t, req), "mount-root")
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	mountFinderFn = func(_ string, _ bool) (string, error) { return "/fake/root", nil }
	storageLookupFn = func(_ string) (storage.Provider, bool) {
		return &fakeProvider{driverName: "fake.csi.example.com", subDir: "nas"}, true
	}
	createSymlinkFn = func(_, _ string) error { return nil }

	// POD_UID env fills the missing pod uid so validateGeneralParams passes.
	assert.NoError(t, runMount(silentCmd()))
}

func TestRunMountPassesConfiguredTimeout(t *testing.T) {
	req := &csi.NodePublishVolumeRequest{
		TargetPath: "/bohr-workspace",
		VolumeContext: map[string]string{
			"csi.storage.k8s.io/pod.uid": "test-pod-uid",
		},
	}
	withRunMountEnv(t, "ossplugin.csi.alibabacloud.com", makeBase64CSIConfig(t, req), "mount-root")
	mountTimeout = 75 * time.Second
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	mountFinderFn = func(_ string, _ bool) (string, error) { return "/fake/root", nil }
	storageLookupFn = func(_ string) (storage.Provider, bool) {
		return &fakeProvider{
			driverName: "ossplugin.csi.alibabacloud.com",
			subDir:     "oss",
			mountFn: func(ctx context.Context, _ csi.NodePublishVolumeRequest) error {
				deadline, ok := ctx.Deadline()
				require.True(t, ok)
				assert.Greater(t, time.Until(deadline), 70*time.Second)
				return nil
			},
		}, true
	}
	createSymlinkFn = func(_, linkPath string) error {
		assert.Equal(t, "/bohr-workspace", linkPath)
		return nil
	}

	assert.NoError(t, runMount(silentCmd()))
}

func TestRunUnmountReleasesMountAndOwnedPaths(t *testing.T) {
	const (
		fakeDriver    = "fake.csi.example.com"
		fakeMountRoot = "/fake/mount-root"
		originTarget  = "/data/workspace"
	)
	req := &csi.NodePublishVolumeRequest{
		VolumeId:   "vol-001",
		TargetPath: originTarget,
		VolumeContext: map[string]string{
			"path": "/tenant/session-a",
		},
	}
	withRunMountEnv(t, fakeDriver, makeBase64CSIConfig(t, req), "mount-root")
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	expectedTarget := "/fake/mount-root/fake/" + getMountTargetHash(fakeDriver, *req)
	var steps []string
	mountFinderFn = func(_ string, _ bool) (string, error) { return fakeMountRoot, nil }
	storageLookupFn = func(_ string) (storage.Provider, bool) {
		return &fakeProvider{
			driverName: fakeDriver,
			subDir:     "fake",
			unmountFn: func(ctx context.Context, got csi.NodePublishVolumeRequest) error {
				deadline, ok := ctx.Deadline()
				require.True(t, ok)
				assert.False(t, deadline.IsZero())
				assert.Equal(t, "vol-001", got.VolumeId)
				assert.Equal(t, expectedTarget, got.TargetPath)
				steps = append(steps, "provider-unmount")
				return nil
			},
		}, true
	}
	removeSymlinkFn = func(target, linkPath string) error {
		assert.Equal(t, expectedTarget, target)
		assert.Equal(t, originTarget, linkPath)
		steps = append(steps, "remove-symlink")
		return nil
	}
	removeMountTargetFn = func(target string) error {
		assert.Equal(t, expectedTarget, target)
		steps = append(steps, "remove-target")
		return nil
	}

	err := runUnmount(silentCmd())

	assert.NoError(t, err)
	assert.Equal(t, []string{"provider-unmount", "remove-symlink", "remove-target"}, steps)
}

func TestRunUnmountStopsCleanupWhenDriverFails(t *testing.T) {
	req := &csi.NodePublishVolumeRequest{VolumeId: "vol-001", TargetPath: "/data/workspace"}
	withRunMountEnv(t, "fake.csi.example.com", makeBase64CSIConfig(t, req), "mount-root")
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	mountFinderFn = func(_ string, _ bool) (string, error) { return "/fake/root", nil }
	storageLookupFn = func(_ string) (storage.Provider, bool) {
		return &fakeProvider{
			driverName: "fake.csi.example.com",
			unmountFn: func(context.Context, csi.NodePublishVolumeRequest) error {
				return fmt.Errorf("target is busy")
			},
		}, true
	}
	removeSymlinkFn = func(string, string) error {
		t.Fatal("symlink cleanup must not run after driver failure")
		return nil
	}
	removeMountTargetFn = func(string) error {
		t.Fatal("target cleanup must not run after driver failure")
		return nil
	}

	err := runUnmount(silentCmd())

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unmount failed for driver")
}

func TestBuiltinStorageProviders(t *testing.T) {
	assert.ElementsMatch(t, []string{
		"nasplugin.csi.alibabacloud.com",
		"ossplugin.csi.alibabacloud.com",
	}, storage.Drivers())
}
