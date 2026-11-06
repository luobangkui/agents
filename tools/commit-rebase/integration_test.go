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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/containerd/containerd"
	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/leases"
	"github.com/containerd/containerd/namespaces"
	"github.com/opencontainers/go-digest"
	specs "github.com/opencontainers/image-spec/specs-go"
	oci "github.com/opencontainers/image-spec/specs-go/v1"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func put(ctx context.Context, c *containerd.Client, b []byte, k string, l map[string]string) oci.Descriptor {
	d := oci.Descriptor{MediaType: k, Digest: digest.FromBytes(b), Size: int64(len(b))}
	if e := content.WriteBlob(ctx, c.ContentStore(), d.Digest.String(), bytes.NewReader(b), d, content.WithLabels(l)); e != nil {
		panic(e)
	}
	return d
}
func fixture(ctx context.Context, c *containerd.Client, n string) {
	ctx, done, e := c.WithLease(ctx)
	if e != nil {
		panic(e)
	}
	defer done(ctx)
	l := put(ctx, c, []byte("fixture-layer"), images.MediaTypeDockerSchema2Layer, nil)
	cfg := oci.Image{Platform: oci.Platform{Architecture: "amd64", OS: "linux"}, RootFS: oci.RootFS{Type: "layers", DiffIDs: []digest.Digest{l.Digest}}}
	b, _ := json.Marshal(cfg)
	cd := put(ctx, c, b, images.MediaTypeDockerSchema2Config, nil)
	m := oci.Manifest{Versioned: specs.Versioned{SchemaVersion: 2}, MediaType: images.MediaTypeDockerSchema2Manifest, Config: cd, Layers: []oci.Descriptor{l}}
	b, _ = json.Marshal(m)
	md := put(ctx, c, b, images.MediaTypeDockerSchema2Manifest, map[string]string{"containerd.io/gc.ref.content.0": cd.Digest.String(), "containerd.io/gc.ref.content.1": l.Digest.String()})
	_, e = c.ImageService().Create(ctx, images.Image{Name: n, Target: md})
	if e != nil {
		panic(e)
	}
}
func valid(ctx context.Context, c *containerd.Client, n string) error {
	im, e := c.ImageService().Get(ctx, n)
	if e != nil {
		return e
	}
	m, e := readManifest(ctx, c.ContentStore(), im.Target)
	if e != nil {
		return fmt.Errorf("manifest: %w", e)
	}
	_, e = readConfig(ctx, c.ContentStore(), m.Config)
	if e != nil {
		return fmt.Errorf("config: %w", e)
	}
	return nil
}

func TestIntegrationLeaseGC(t *testing.T) {
	socket := os.Getenv("REBASE_TEST_SOCKET")
	if socket == "" {
		t.Skip("isolated daemon required")
	}
	if !strings.HasPrefix(socket, "/tmp/relay-commit-digest-") {
		t.Fatal("refusing non-isolated socket")
	}
	c, e := containerd.New(socket)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	ctx := namespaces.WithNamespace(context.Background(), "lease-test")
	fixture(ctx, c, "base")
	fixture(ctx, c, "sentinel")
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			im, e := c.ImageService().Get(ctx, "sentinel")
			if e != nil {
				return
			}
			im.Name = "gc-trigger"
			if _, e = c.ImageService().Create(ctx, im); e != nil {
				return
			}
			if e = c.ImageService().Delete(ctx, "gc-trigger", images.SynchronousDelete()); e != nil {
				return
			}
		}
	}()
	defer func() { close(stop); wg.Wait() }()
	for i := 0; i < 64; i++ {
		ref := fmt.Sprintf("committed-%d", i)
		fixture(ctx, c, ref)
		before, e := c.LeasesService().List(ctx)
		if e != nil {
			t.Fatal(e)
		}
		if e = rebase(ctx, c, ref, "base"); e != nil {
			t.Fatal(e)
		}
		if e = valid(ctx, c, ref); e != nil {
			t.Fatal(e)
		}
		after, e := c.LeasesService().List(ctx)
		if e != nil {
			t.Fatal(e)
		}
		if len(after) != len(before) {
			t.Fatalf("lease leak %d -> %d", len(before), len(after))
		}
		if e = c.ImageService().Delete(ctx, ref); e != nil {
			t.Fatal(e)
		}
	}
	// Error paths release the real lease even with an already-cancelled context.
	if e = withRebaseLease(ctx, c.LeasesService(), func(op context.Context) error { return fmt.Errorf("injected failure") }); e == nil {
		t.Fatal("expected error")
	}
	all, e := c.LeasesService().List(ctx)
	if e != nil || len(all) != 0 {
		t.Fatalf("leases after failure=%v err=%v", all, e)
	}
	// Validate server-side expiry actually collects an abandoned lease.
	abandoned, e := c.LeasesService().Create(ctx, leases.WithRandomID(), leases.WithExpiration(time.Second))
	if e != nil {
		t.Fatal(e)
	}
	until := time.Now().Add(15 * time.Second)
	for {
		all, e = c.LeasesService().List(ctx)
		if e != nil {
			t.Fatal(e)
		}
		found := false
		for _, l := range all {
			if l.ID == abandoned.ID {
				found = true
			}
		}
		if !found {
			break
		}
		if time.Now().After(until) {
			t.Fatal("expired lease retained")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestLeaseProcessHelper is a separate OS process to exercise signal handling.
func TestLeaseProcessHelper(t *testing.T) {
	if os.Getenv("REBASE_LEASE_CHILD") != "1" {
		t.Skip("child only")
	}
	socket := os.Getenv("REBASE_TEST_SOCKET")
	if !strings.HasPrefix(socket, "/tmp/relay-commit-digest-") {
		t.Fatal("isolated socket required")
	}
	c, e := containerd.New(socket)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()
	ctx = namespaces.WithNamespace(ctx, os.Getenv("REBASE_CHILD_NS"))
	e = withRebaseLease(ctx, c.LeasesService(), func(ctx context.Context) error {
		fmt.Println("LEASE_READY")
		<-ctx.Done()
		return ctx.Err()
	})
	if e != context.Canceled {
		t.Fatalf("unexpected child result %v", e)
	}
}
func TestProcessLeaseExit(t *testing.T) {
	socket := os.Getenv("REBASE_TEST_SOCKET")
	if socket == "" {
		t.Skip("isolated daemon required")
	}
	if !strings.HasPrefix(socket, "/tmp/relay-commit-digest-") {
		t.Fatal("isolated socket required")
	}
	c, e := containerd.New(socket)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL} {
		t.Run(sig.String(), func(t *testing.T) {
			ns := fmt.Sprintf("process-exit-%d", sig)
			ctx := namespaces.WithNamespace(context.Background(), ns)
			cmd := exec.Command(os.Args[0], "-test.run=^TestLeaseProcessHelper$", "-test.timeout=30s")
			cmd.Env = append(os.Environ(), "REBASE_LEASE_CHILD=1", "REBASE_CHILD_NS="+ns)
			stdout, e := cmd.StdoutPipe()
			if e != nil {
				t.Fatal(e)
			}
			cmd.Stderr = os.Stderr
			if e = cmd.Start(); e != nil {
				t.Fatal(e)
			}
			defer func() {
				if cmd.ProcessState == nil {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				}
			}()
			ready := make(chan bool, 1)
			go func() {
				scan := bufio.NewScanner(stdout)
				for scan.Scan() {
					if scan.Text() == "LEASE_READY" {
						ready <- true
						return
					}
				}
				ready <- false
			}()
			select {
			case ok := <-ready:
				if !ok {
					t.Fatal("child did not create lease")
				}
			case <-time.After(15 * time.Second):
				t.Fatal("child timeout")
			}
			all, e := c.LeasesService().List(ctx)
			if e != nil || len(all) != 1 {
				t.Fatalf("child lease=%v err=%v", all, e)
			}
			if e = cmd.Process.Signal(sig); e != nil {
				t.Fatal(e)
			}
			waitErr := cmd.Wait()
			all, e = c.LeasesService().List(ctx)
			if e != nil {
				t.Fatal(e)
			}
			if sig == syscall.SIGTERM {
				if waitErr != nil || len(all) != 0 {
					t.Fatalf("graceful exit leaked: err=%v leases=%v", waitErr, all)
				}
			} else {
				if waitErr == nil || len(all) != 1 {
					t.Fatalf("kill did not leave expected bounded lease: %v", all)
				}
				expires, e := time.Parse(time.RFC3339, all[0].Labels["containerd.io/gc.expire"])
				if e != nil || time.Until(expires) <= 0 || time.Until(expires) > rebaseLeaseTTL {
					t.Fatal("killed process lease has no bounded expiry")
				}
				// Cleanup our fixture; server-side expiry is exercised separately above.
				if e = c.LeasesService().Delete(ctx, all[0]); e != nil {
					t.Fatal(e)
				}
			}
		})
	}
}
