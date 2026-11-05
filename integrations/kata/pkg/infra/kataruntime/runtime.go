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
package kataruntime

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/containerd/containerd"
	tasksapi "github.com/containerd/containerd/api/services/tasks/v1"
	"github.com/containerd/containerd/cio"
	"github.com/containerd/containerd/containers"
	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/namespaces"
	"github.com/containerd/containerd/oci"
	runtimeoptions "github.com/containerd/containerd/pkg/runtimeoptions/v1"
	"github.com/containerd/containerd/snapshots"
	specs "github.com/opencontainers/runtime-spec/specs-go"
	lifecycle "github.com/openkruise/agents/katalab/pkg/infra"
	"github.com/openkruise/agents/pkg/sandbox-manager/infra"
)

const Image = "m.daocloud.io/docker.io/library/python@sha256:e697c81897302a4c4333bade000d98038fbb21eb064f8ee1fd42d02ebcd9d337"

var safeID = regexp.MustCompile(`^ta[0-9a-z-]{8,60}$`)

type Config struct{ Root, Socket string }
type Backend struct {
	cfg    Config
	client *containerd.Client
	mu     sync.Mutex
	lock   *os.File
}
type CreateSpec = lifecycle.CreateSpec
type Record struct {
	Spec      CreateSpec                `json:"spec"`
	Target    infra.ServiceTarget       `json:"target"`
	Phase     string                    `json:"phase"`
	VSock     string                    `json:"vsock"`
	Boot      string                    `json:"boot"`
	IDs       []string                  `json:"ids"`
	Proofs    map[string]map[string]any `json:"proofs"`
	Stops     map[string]*Stop          `json:"stops"`
	Reclaimed bool                      `json:"reclaimed"`
}
type Stop struct {
	State                  string              `json:"state"`
	RequestID              string              `json:"request_id"`
	RequestedAt            time.Time           `json:"requested_at"`
	Target                 infra.ServiceTarget `json:"target"`
	Service                string              `json:"service"`
	Before, ObserverBefore map[string]any
	Receipt                json.RawMessage `json:"receipt,omitempty"`
}

func nonce() string {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func Open(cfg Config) (*Backend, error) {
	if cfg.Root != "/work/api-state" || cfg.Socket != "/work/containerd/state/containerd.sock" {
		return nil, fmt.Errorf("private test profile paths required")
	}
	if e := os.MkdirAll(cfg.Root, 0700); e != nil {
		return nil, e
	}
	f, e := os.OpenFile(filepath.Join(cfg.Root, ".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		f.Close()
		return nil, fmt.Errorf("single writer required: %w", e)
	}
	c, e := containerd.New(cfg.Socket)
	if e != nil {
		f.Close()
		return nil, e
	}
	return &Backend{cfg: cfg, client: c, lock: f}, nil
}
func (b *Backend) Close() error         { return errors.Join(b.client.Close(), b.lock.Close()) }
func (b *Backend) dir(id string) string { return filepath.Join(b.cfg.Root, id) }
func atomic(path string, v any) error {
	data, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".pending-")
	if e != nil {
		return e
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, e = f.Write(append(data, '\n')); e != nil {
		f.Close()
		return e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	if e = os.Rename(tmp, path); e != nil {
		return e
	}
	d, e := os.Open(filepath.Dir(path))
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}
func (b *Backend) save(r *Record) error {
	return atomic(filepath.Join(b.dir(r.Spec.ExecutionID), "record.json"), r)
}
func (b *Backend) load(id string) (*Record, error) {
	if !safeID.MatchString(id) {
		return nil, fmt.Errorf("invalid execution id")
	}
	data, e := os.ReadFile(filepath.Join(b.dir(id), "record.json"))
	if e != nil {
		return nil, e
	}
	var r Record
	e = json.Unmarshal(data, &r)
	if e != nil {
		return nil, e
	}
	if r.Spec.ExecutionID != id {
		return nil, fmt.Errorf("state identity mismatch")
	}
	return &r, nil
}
func (b *Backend) Inspect(id string) (*Record, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.load(id)
}
func bind(src, dst string, ro bool) specs.Mount {
	o := []string{"rbind"}
	if ro {
		o = append(o, "ro")
	}
	return specs.Mount{Type: "bind", Source: src, Destination: dst, Options: o}
}
func (b *Backend) create(ctx context.Context, r *Record, image containerd.Image, role, sandbox string, args []string, limit int64, mounts []specs.Mount) (containerd.Task, error) {
	id := r.Spec.ExecutionID + "-" + role
	r.IDs = append(r.IDs, id)
	if e := b.save(r); e != nil {
		return nil, e
	}
	opt := func(ctx context.Context, c oci.Client, cont *containers.Container, s *oci.Spec) error {
		s.Process.Args = args
		s.Process.Cwd = "/"
		s.Process.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=/tmp"}
		s.Process.User = specs.User{UID: 10001, GID: 10001}
		s.Process.NoNewPrivileges = true
		s.Process.Capabilities = &specs.LinuxCapabilities{}
		s.Root.Readonly = role == "observer" || role == "pause"
		s.Annotations = map[string]string{"io.kubernetes.cri.container-type": "container", "io.kubernetes.cri.sandbox-id": sandbox, "io.kubernetes.cri.container-name": role}
		if sandbox == "" {
			s.Annotations["io.kubernetes.cri.container-type"] = "sandbox"
			delete(s.Annotations, "io.kubernetes.cri.sandbox-id")
		}
		if role == "observer" {
			s.Process.User = specs.User{}
			if r.Spec.Fault != "no-raw" {
				s.Process.Capabilities = &specs.LinuxCapabilities{Bounding: []string{"CAP_NET_RAW"}, Effective: []string{"CAP_NET_RAW"}, Permitted: []string{"CAP_NET_RAW"}}
			}
		}
		if r.Spec.Verifier {
			s.Process.User = specs.User{}
		}
		s.Linux.Namespaces = append(s.Linux.Namespaces, specs.LinuxNamespace{Type: specs.CgroupNamespace})
		s.Mounts = append(s.Mounts, specs.Mount{Type: "cgroup", Source: "cgroup", Destination: "/sys/fs/cgroup", Options: []string{"nosuid", "noexec", "nodev", "ro"}})
		s.Linux.CgroupsPath = "/taskarena/" + id
		memory := int64(512 * 1024 * 1024)
		quota := int64(100000)
		period := uint64(100000)
		if role == "pause" {
			memory = 16 * 1024 * 1024
			quota = 10000
		}
		if role == "observer" {
			memory = 128 * 1024 * 1024
			quota = 50000
		}
		s.Linux.Resources = &specs.LinuxResources{Memory: &specs.LinuxMemory{Limit: &memory}, CPU: &specs.LinuxCPU{Quota: &quota, Period: &period}}
		if limit > 0 {
			s.Linux.Resources.Pids = &specs.LinuxPids{Limit: limit}
		}
		s.Mounts = append(s.Mounts, mounts...)
		return nil
	}
	c, e := b.client.NewContainer(ctx, id, containerd.WithImage(image), containerd.WithSnapshotter("native"), containerd.WithNewSnapshot(id, image), containerd.WithNewSpec(oci.WithImageConfig(image), opt), containerd.WithRuntime("io.containerd.kata.v2", &runtimeoptions.Options{ConfigPath: "/work/kata.toml"}))
	if e != nil {
		return nil, e
	}
	sp, e := c.Spec(ctx)
	if e != nil {
		return nil, e
	}
	if e = atomic(filepath.Join(b.dir(r.Spec.ExecutionID), role+"-oci.json"), sp); e != nil {
		return nil, e
	}
	logpath := filepath.Join(b.dir(r.Spec.ExecutionID), role+".log")
	t, e := c.NewTask(ctx, cio.BinaryIO("/work/kata-logger.py", map[string]string{"output": logpath}))
	if e != nil {
		return nil, e
	}
	if sandbox == "" {
		control, e := os.ReadFile("/work/runtime-ns.pid")
		if e != nil {
			return nil, e
		}
		out, e := exec.CommandContext(ctx, "nsenter", "-t", strings.TrimSpace(string(control)), "--mount", "--cgroup", "python3", "/work/move_shim.py", id).CombinedOutput()
		if e != nil {
			return nil, fmt.Errorf("private shim placement: %w: %s", e, out)
		}
		if e = os.WriteFile(filepath.Join(b.dir(r.Spec.ExecutionID), role+"-placement.json"), out, 0600); e != nil {
			return nil, e
		}
	}
	return t, nil
}
func findVSock(sandbox string) (string, error) {
	paths, e := filepath.Glob("/proc/[0-9]*/cmdline")
	if e != nil {
		return "", e
	}
	re := regexp.MustCompile(`guest-cid=([0-9]+)`)
	found := []string{}
	for _, p := range paths {
		data, e := os.ReadFile(p)
		if e != nil {
			continue
		}
		parts := strings.Split(string(data), "\x00")
		if len(parts) == 0 || !strings.Contains(parts[0], "qemu-system") {
			continue
		}
		owned := false
		for _, v := range parts {
			if v == "sandbox-"+sandbox || v == sandbox {
				owned = true
			}
		}
		if !owned {
			continue
		}
		m := re.FindStringSubmatch(string(data))
		if len(m) == 2 {
			found = append(found, "vsock://"+m[1]+":1024")
		}
	}
	if len(found) != 1 {
		return "", fmt.Errorf("expected one owned QEMU, found %d", len(found))
	}
	return found[0], nil
}
func (b *Backend) proof(ctx context.Context, r *Record, cid string) (map[string]any, error) {
	address, e := findVSock(r.Spec.ExecutionID + "-pause")
	if e != nil {
		return nil, e
	}
	if r.VSock != "" && address != r.VSock {
		return nil, fmt.Errorf("VM vsock identity changed")
	}
	n := nonce()
	req, _ := json.Marshal(map[string]string{"container_id": cid, "log_type": "taskarena_cgroup_v1:" + n})
	out, e := exec.CommandContext(ctx, "/work/artifacts/kata-agent-ctl", "-l", "error", "connect", "--no-auto-values", "true", "--timeout", "10s", "--server-address", address, "-c", "GetDiagnosticData json://"+string(req)).CombinedOutput()
	if e != nil {
		return nil, fmt.Errorf("trusted guest proof: %w: %s", e, out)
	}
	var p map[string]any
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "TASKARENA_PROOF=") {
			if e = json.Unmarshal([]byte(strings.TrimPrefix(line, "TASKARENA_PROOF=")), &p); e != nil {
				return nil, e
			}
		}
	}
	if e = validateProof(p, cid, n, r.Boot); e != nil {
		return nil, e
	}
	return p, nil
}
func (b *Backend) Prepare(ctx context.Context, s CreateSpec) (infra.ServiceTarget, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	empty := infra.ServiceTarget{}
	if !safeID.MatchString(s.ExecutionID) {
		return empty, fmt.Errorf("invalid execution id")
	}
	if s.Fault != "" && s.Fault != "ignore-term" && s.Fault != "no-raw" {
		return empty, fmt.Errorf("unsupported fault")
	}
	if old, e := b.load(s.ExecutionID); e == nil {
		if !reflect.DeepEqual(old.Spec, s) || old.Phase != "Running" || old.Reclaimed {
			return old.Target, fmt.Errorf("create requires recovery; automatic rerun forbidden")
		}
		return old.Target, nil
	} else if !os.IsNotExist(e) {
		return empty, e
	}
	ctx, cancel := context.WithTimeout(namespaces.WithNamespace(ctx, s.ExecutionID), 90*time.Second)
	defer cancel()
	if e := os.Mkdir(b.dir(s.ExecutionID), 0700); e != nil {
		return empty, e
	}
	r := &Record{Spec: s, Phase: "Preparing", Proofs: map[string]map[string]any{}, Stops: map[string]*Stop{}, Target: infra.ServiceTarget{ExecutionID: s.ExecutionID, OwnerID: nonce(), Containers: map[string]string{}}}
	if e := b.save(r); e != nil {
		return empty, e
	}
	cached, e := b.client.GetImage(namespaces.WithNamespace(ctx, "ta-kata-cache"), Image)
	if e != nil {
		return r.Target, fmt.Errorf("frozen image must be prewarmed: %w", e)
	}
	descriptor := cached.Target()
	if descriptor.Digest.String() != "sha256:e697c81897302a4c4333bade000d98038fbb21eb064f8ee1fd42d02ebcd9d337" || (descriptor.MediaType != "application/vnd.oci.image.index.v1+json" && descriptor.MediaType != "application/vnd.docker.distribution.manifest.list.v2+json") {
		return r.Target, fmt.Errorf("cached image descriptor mismatch")
	}
	archive, e := os.Open("/work/base-image.oci.tar")
	if e != nil {
		return r.Target, e
	}
	_, importErr := b.client.Import(ctx, archive, containerd.WithSkipMissing())
	closeErr := archive.Close()
	if importErr != nil {
		return r.Target, importErr
	}
	if closeErr != nil {
		return r.Target, closeErr
	}
	image, e := b.client.GetImage(ctx, Image)
	if e != nil {
		return r.Target, e
	}
	if image.Target().Digest != descriptor.Digest {
		return r.Target, fmt.Errorf("imported digest mismatch")
	}
	if e = image.Unpack(ctx, "native"); e != nil {
		return r.Target, e
	}
	pause, e := b.create(ctx, r, image, "pause", "", []string{"python", "-c", "import time;time.sleep(5000)"}, 0, nil)
	if e != nil {
		return r.Target, e
	}
	if e = pause.Start(ctx); e != nil {
		return r.Target, e
	}
	r.VSock, e = findVSock(pause.ID())
	if e != nil {
		return r.Target, e
	}
	for _, d := range []string{"app", "logs", "solution", "tmp"} {
		p := filepath.Join(b.dir(s.ExecutionID), d)
		if e = os.Mkdir(p, 0777); e != nil {
			return r.Target, e
		}
		if e = os.Chmod(p, 0777); e != nil {
			return r.Target, e
		}
	}
	script, e := os.ReadFile("/work/fixture/task/environment/send_probe.py")
	if e != nil {
		return r.Target, e
	}
	if e = os.WriteFile(filepath.Join(b.dir(s.ExecutionID), "app/send_probe.py"), script, 0644); e != nil {
		return r.Target, e
	}
	mainMounts := []specs.Mount{bind(b.dir(s.ExecutionID)+"/app", "/app", false), bind(b.dir(s.ExecutionID)+"/logs", "/logs", false), bind(b.dir(s.ExecutionID)+"/solution", "/solution", false), bind(b.dir(s.ExecutionID)+"/tmp", "/tmp", false)}
	if s.Verifier {
		mainMounts = append(mainMounts, bind("/work/fixture/task/tests", "/tests", true))
	} else {
		obs, e := b.create(ctx, r, image, "observer", pause.ID(), []string{"python", "-u", "/observer/monitor.py"}, 32, []specs.Mount{bind("/work/fixture/task/environment/observer", "/observer", true), {Type: "tmpfs", Source: "tmpfs", Destination: "/audit", Options: []string{"size=4m", "mode=0700", "nosuid", "nodev"}}})
		if e != nil {
			return r.Target, e
		}
		p, e := b.proof(ctx, r, obs.ID())
		if e != nil {
			return r.Target, e
		}
		if p["pids_max"] != "32" || p["requested_pid_limit"] != float64(32) {
			return r.Target, fmt.Errorf("PID32 not enforced before start")
		}
		r.Boot = p["guest_boot_id"].(string)
		r.Target.Containers["observer"] = obs.ID()
		r.Proofs["observer_before_start"] = p
		if e = b.save(r); e != nil {
			return r.Target, e
		}
		if e = obs.Start(ctx); e != nil {
			return r.Target, e
		}
	}
	args := []string{"python", "-c", "import time;time.sleep(5000)"}
	if s.Fault == "ignore-term" {
		args = []string{"python", "-u", "/probe/main.py"}
		mainMounts = append(mainMounts, bind("/work/checks", "/probe", true))
	}
	main, e := b.create(ctx, r, image, "main", pause.ID(), args, 0, mainMounts)
	if e != nil {
		return r.Target, e
	}
	r.Target.Containers["main"] = main.ID()
	p, e := b.proof(ctx, r, main.ID())
	if e != nil {
		return r.Target, e
	}
	if r.Boot == "" {
		r.Boot = p["guest_boot_id"].(string)
	}
	r.Proofs["main_before_start"] = p
	r.Target.InstanceID = r.Boot
	if e = main.Start(ctx); e != nil {
		return r.Target, e
	}
	if !s.Verifier {
		ready := false
		for i := 0; i < 25; i++ {
			res, e := b.execute(ctx, r, "observer", []string{"python", "-c", "from pathlib import Path;assert Path('/audit/ready').exists()"}, "0:0")
			if e != nil {
				return r.Target, e
			}
			if res.ExitCode == 0 {
				ready = true
				break
			}
			select {
			case <-ctx.Done():
				return r.Target, ctx.Err()
			case <-time.After(200 * time.Millisecond):
			}
		}
		if !ready {
			return r.Target, fmt.Errorf("observer readiness failed")
		}
		p, e = b.proof(ctx, r, r.Target.Containers["observer"])
		if e != nil {
			return r.Target, e
		}
		r.Proofs["observer_running"] = p
	}
	policy, e := json.Marshal(map[string]any{"policy": "pids32-kata", "runtime": "kata-runtime-rs-taskarena-test", "image": Image, "guest_boot_id": r.Boot, "proofs": r.Proofs})
	if e != nil {
		return r.Target, e
	}
	r.Target.PolicyReceipt = policy
	r.Phase = "Running"
	return r.Target, b.save(r)
}
func sameCgroup(a, b map[string]any) bool {
	for _, k := range []string{"guest_boot_id", "container_id", "cgroup_path", "cgroup_device", "cgroup_inode"} {
		if a[k] != b[k] {
			return false
		}
	}
	return true
}
func empty(p map[string]any) bool {
	v, ok := p["processes"].([]any)
	return ok && len(v) == 0 && p["pids_current"] == float64(0) && p["populated"] == float64(0)
}
func (b *Backend) task(ctx context.Context, r *Record, service string) (containerd.Task, *specs.Spec, error) {
	id := r.Target.Containers[service]
	if id == "" {
		return nil, nil, fmt.Errorf("service missing")
	}
	c, e := b.client.LoadContainer(ctx, id)
	if e != nil {
		return nil, nil, e
	}
	s, e := c.Spec(ctx)
	if e != nil {
		return nil, nil, e
	}
	t, e := c.Task(ctx, nil)
	return t, s, e
}

type bounded struct {
	bytes.Buffer
	over bool
}

func (b *bounded) Write(p []byte) (int, error) {
	n := len(p)
	if b.Len()+n > 1024*1024 {
		b.over = true
		left := 1024*1024 - b.Len()
		if left > 0 {
			b.Buffer.Write(p[:left])
		}
		return n, nil
	}
	return b.Buffer.Write(p)
}
func (b *Backend) execute(ctx context.Context, r *Record, service string, args []string, user string) (result infra.ServiceResult, retErr error) {
	if len(args) == 0 {
		return result, fmt.Errorf("command required")
	}
	t, s, e := b.task(ctx, r, service)
	if e != nil {
		return result, e
	}
	proc := *s.Process
	proc.Args = args
	switch user {
	case "", "10001:10001":
		if service == "main" && !r.Spec.Verifier {
			proc.User = specs.User{UID: 10001, GID: 10001}
		}
	case "0:0":
		proc.User = specs.User{}
	default:
		return result, fmt.Errorf("unsupported uid")
	}
	var out, errout bounded
	p, e := t.Exec(ctx, "exec-"+nonce()[:16], &proc, cio.NewCreator(cio.WithStreams(nil, &out, &errout)))
	if e != nil {
		return result, e
	}
	defer func() {
		clean, cancel := context.WithTimeout(context.WithoutCancel(ctx), 8*time.Second)
		defer cancel()
		if ctx.Err() != nil {
			ke := p.Kill(clean, syscall.SIGKILL)
			if ke != nil && !errdefs.IsNotFound(ke) {
				retErr = errors.Join(retErr, ke)
			}
		}
		_, de := p.Delete(clean)
		if de != nil && !errdefs.IsNotFound(de) {
			retErr = errors.Join(retErr, de)
		}
	}()
	ch, e := p.Wait(ctx)
	if e != nil {
		return result, e
	}
	if e = p.Start(ctx); e != nil {
		return result, e
	}
	select {
	case st := <-ch:
		code, _, e := st.Result()
		p.IO().Wait()
		if e != nil {
			return result, e
		}
		if out.over || errout.over {
			return result, fmt.Errorf("output quota exceeded")
		}
		result.Stdout = out.Bytes()
		result.Stderr = errout.Bytes()
		result.ExitCode = int(code)
		return result, nil
	case <-ctx.Done():
		return result, ctx.Err()
	}
}
func (b *Backend) ExecuteService(ctx context.Context, target infra.ServiceTarget, a infra.ServiceAction) (infra.ServiceResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	result := infra.ServiceResult{}
	r, e := b.load(target.ExecutionID)
	if e != nil {
		return result, e
	}
	if !sameTarget(target, r.Target) || r.Reclaimed || r.Phase != "Running" {
		return result, fmt.Errorf("immutable runtime binding mismatch or unavailable")
	}
	ctx = namespaces.WithNamespace(ctx, target.ExecutionID)
	if a.Kind == "stop" {
		return b.stop(ctx, r, a.Service)
	}
	if a.Kind == "collect" {
		if a.Service != "observer" || r.Stops["main"] == nil || r.Stops["main"].State != "Succeeded" {
			return result, fmt.Errorf("proven main stop required")
		}
		a.Args = []string{"python", "/observer/collect.py"}
		a.User = "0:0"
	}
	if a.Kind == "download" {
		allowed := a.Service == "main" && a.Source == "/app/result.json" || a.Service == "observer" && a.Source == "/audit/network-audit.json"
		if !allowed {
			return result, fmt.Errorf("undeclared artifact")
		}
		a.Args = []string{"python", "-c", "import os,stat,sys;f=os.open(sys.argv[1],os.O_RDONLY|os.O_NOFOLLOW);s=os.fstat(f);assert stat.S_ISREG(s.st_mode) and s.st_size<=524288;sys.stdout.buffer.write(os.read(f,524289))", a.Source}
		a.User = "0:0"
	}
	if a.Kind != "exec" && a.Kind != "collect" && a.Kind != "download" {
		return result, fmt.Errorf("unsupported action")
	}
	return b.execute(ctx, r, a.Service, a.Args, a.User)
}
func (b *Backend) stop(ctx context.Context, r *Record, service string) (infra.ServiceResult, error) {
	result := infra.ServiceResult{}
	if service != "main" && service != "observer" {
		return result, fmt.Errorf("unknown service")
	}
	clean, cancel := context.WithTimeout(context.WithoutCancel(ctx), 35*time.Second)
	defer cancel()
	ctx = clean
	st := r.Stops[service]
	if st != nil && st.State == "Succeeded" {
		result.Receipt = st.Receipt
		return result, nil
	}
	t, _, e := b.task(ctx, r, service)
	if e != nil {
		return result, e
	}
	if st == nil {
		before, e := b.proof(ctx, r, r.Target.Containers[service])
		if e != nil {
			return result, e
		}
		var observer map[string]any
		if service == "main" && !r.Spec.Verifier {
			observer, e = b.proof(ctx, r, r.Target.Containers["observer"])
			if e != nil {
				return result, e
			}
		}
		st = &Stop{State: "Unknown", RequestID: nonce(), RequestedAt: time.Now().UTC(), Target: r.Target, Service: service, Before: before, ObserverBefore: observer}
		r.Stops[service] = st
		if e = b.save(r); e != nil {
			return result, e
		}
	}
	status, e := t.Status(ctx)
	if e != nil {
		return result, e
	}
	if status.Status != containerd.Stopped {
		if e = t.Kill(ctx, syscall.SIGKILL, containerd.WithKillAll); e != nil {
			return result, e
		}
	}
	wait, e := t.Wait(ctx)
	if e != nil {
		return result, e
	}
	var code uint32
	var at time.Time
	select {
	case done := <-wait:
		code, at, e = done.Result()
		if e != nil {
			return result, e
		}
	case <-ctx.Done():
		return result, ctx.Err()
	}
	var final map[string]any
	for i := 0; i < 20; i++ {
		final, e = b.proof(ctx, r, r.Target.Containers[service])
		if e != nil {
			return result, e
		}
		if empty(final) && sameCgroup(st.Before, final) {
			break
		}
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	if !empty(final) || !sameCgroup(st.Before, final) {
		return result, fmt.Errorf("target cgroup not proven empty")
	}
	var observer map[string]any
	if st.ObserverBefore != nil {
		observer, e = b.proof(ctx, r, r.Target.Containers["observer"])
		if e != nil {
			return result, e
		}
		if !sameCgroup(st.ObserverBefore, observer) || observer["populated"] != float64(1) || observer["pids_max"] != "32" {
			return result, fmt.Errorf("observer continuity lost")
		}
	}
	receipt, e := json.Marshal(map[string]any{"schema_version": 1, "source": "kata-runtime-stop-v1", "request_id": st.RequestID, "requested_at": st.RequestedAt, "target": r.Target, "service": service, "exit_code": code, "exited_at": at, "main_before": st.Before, "final_proof": final, "observer_before": st.ObserverBefore, "observer_after": observer, "confirmed_at": time.Now().UTC(), "method": "containerd.Task.Kill(SIGKILL,all)"})
	if e != nil {
		return result, e
	}
	st.State = "Succeeded"
	st.Receipt = receipt
	if e = b.save(r); e != nil {
		return result, e
	}
	result.Receipt = receipt
	return result, nil
}
func (b *Backend) RecoverStop(ctx context.Context, target infra.ServiceTarget, service string) (infra.ServiceResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	r, e := b.load(target.ExecutionID)
	if e != nil {
		return infra.ServiceResult{}, e
	}
	if !sameTarget(target, r.Target) || r.Stops[service] == nil {
		return infra.ServiceResult{}, fmt.Errorf("matching durable stop intent missing")
	}
	if r.Stops[service].State == "Succeeded" {
		return infra.ServiceResult{Receipt: r.Stops[service].Receipt}, nil
	}
	ctx = namespaces.WithNamespace(ctx, target.ExecutionID)
	t, _, e := b.task(ctx, r, service)
	if e != nil {
		return infra.ServiceResult{}, e
	}
	status, e := t.Status(ctx)
	if e != nil {
		return infra.ServiceResult{}, e
	}
	if status.Status != containerd.Stopped {
		return infra.ServiceResult{}, fmt.Errorf("stop remains Unknown; target is not stopped")
	}
	return b.stop(ctx, r, service)
}
func (b *Backend) Reclaim(ctx context.Context, id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	r, e := b.load(id)
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	if r.Reclaimed {
		return nil
	}
	ctx = namespaces.WithNamespace(ctx, id)
	for i := len(r.IDs) - 1; i >= 0; i-- {
		c, e := b.client.LoadContainer(ctx, r.IDs[i])
		if errdefs.IsNotFound(e) {
			continue
		}
		if e != nil {
			return e
		}
		t, e := c.Task(ctx, nil)
		if e == nil {
			deleteCtx, done := context.WithTimeout(ctx, 8*time.Second)
			_, e = t.Delete(deleteCtx, containerd.WithProcessKill)
			done()
			if e != nil && !errdefs.IsNotFound(e) {
				// Whole-execution reclaim can destroy its VM. A service stop
				// never reaches this fallback and must preserve the observer.
				control, ce := os.ReadFile("/work/runtime-ns.pid")
				if ce != nil {
					return ce
				}
				out, ce := exec.CommandContext(ctx, "nsenter", "-t", strings.TrimSpace(string(control)), "--mount", "--cgroup", "python3", "/work/reclaim_owned.py", id).CombinedOutput()
				if ce != nil {
					return fmt.Errorf("private forced reclaim: %w: %s", ce, out)
				}
				// Let containerd consume the exact shim's exit and remove its task.
				for n := 0; n < 30; n++ {
					_, ce = c.Task(ctx, nil)
					if errdefs.IsNotFound(ce) {
						break
					}
					select {
					case <-ctx.Done():
						return ctx.Err()
					case <-time.After(100 * time.Millisecond):
					}
				}
				if ce != nil && !errdefs.IsNotFound(ce) {
					return ce
				}
			}
		} else if !errdefs.IsNotFound(e) {
			return e
		}
		if e = c.Delete(ctx, containerd.WithSnapshotCleanup); e != nil && !errdefs.IsNotFound(e) {
			return e
		}
	}
	controlPID, ce := os.ReadFile("/work/runtime-ns.pid")
	if ce != nil {
		return ce
	}
	cleanupOut, ce := exec.CommandContext(ctx, "nsenter", "-t", strings.TrimSpace(string(controlPID)), "--mount", "--cgroup", "python3", "/work/reclaim_owned.py", id).CombinedOutput()
	if ce != nil {
		return fmt.Errorf("private residual cleanup: %w: %s", ce, cleanupOut)
	}
	left, e := b.client.Containers(ctx)
	if e != nil {
		return e
	}
	if len(left) != 0 {
		return fmt.Errorf("containers remain")
	}
	remainingTasks, e := b.client.TaskService().List(ctx, &tasksapi.ListTasksRequest{})
	if e != nil {
		return e
	}
	if len(remainingTasks.Tasks) != 0 {
		return fmt.Errorf("task metadata remains")
	}

	// The agent receipt proves guest cleanup; separately prove host-side private
	// runtime resources are gone. Missing telemetry must not mean empty.
	var snapshotIDs []string
	e = b.client.SnapshotService("native").Walk(ctx, func(_ context.Context, info snapshots.Info) error {
		if info.Kind == snapshots.KindActive || info.Kind == snapshots.KindView {
			snapshotIDs = append(snapshotIDs, info.Name)
			return nil
		}
		for _, owned := range r.IDs {
			if info.Name == owned {
				snapshotIDs = append(snapshotIDs, info.Name)
			}
		}
		return nil
	})
	if e != nil {
		return e
	}
	if len(snapshotIDs) != 0 {
		return fmt.Errorf("owned snapshots remain: %v", snapshotIDs)
	}
	control, e := os.ReadFile("/work/runtime-ns.pid")
	if e != nil {
		return e
	}
	code := `import pathlib,sys,json
r=pathlib.Path('/sys/fs/cgroup');sid=sys.argv[1];assert (r/'memory.max').read_text().strip()=='8589934592'
left=[]
for p in pathlib.Path('/proc').glob('[0-9]*/cmdline'):
 try:
  args=p.read_bytes().split(b'\0');exe=(p.parent/'exe').resolve().name
  if (exe in ['qemu-system-x86_64','containerd-shim-kata-v2','virtiofsd'] or b'/work/kata-logger.py' in args) and any(sid.encode() in x for x in args):left.append(str(p.parent))
 except (FileNotFoundError,ProcessLookupError):pass
cg=[str(p) for p in (r/'taskarena').glob(sid+'*')] if (r/'taskarena').exists() else []
assert not left and not cg, (left,cg)
print(json.dumps({'processes':left,'cgroups':cg,'scope':sid}))`
	out, e := exec.CommandContext(ctx, "nsenter", "-t", strings.TrimSpace(string(control)), "--mount", "--cgroup", "python3", "-c", code, id).CombinedOutput()
	if e != nil {
		return fmt.Errorf("private runtime cleanup unconfirmed: %w: %s", e, out)
	}
	if e = os.WriteFile(filepath.Join(b.dir(id), "cleanup-proof.json"), out, 0600); e != nil {
		return e
	}
	r.Reclaimed = true
	r.Phase = "Reclaimed"
	return b.save(r)
}

func (b *Backend) State(ctx context.Context, id string) (json.RawMessage, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	r, e := b.Inspect(id)
	if e != nil {
		return nil, e
	}
	return json.Marshal(r)
}

// RawMessage formatting changes across durable JSON and HTTP encodings. Compare
// the complete policy value, while retaining all immutable incarnation fields.
func sameTarget(a, b infra.ServiceTarget) bool {
	var ap, bp any
	if json.Unmarshal(a.PolicyReceipt, &ap) != nil || json.Unmarshal(b.PolicyReceipt, &bp) != nil {
		return false
	}
	a.PolicyReceipt = nil
	b.PolicyReceipt = nil
	return reflect.DeepEqual(a, b) && reflect.DeepEqual(ap, bp)
}
