package runtime

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/OpceanAI/Doki/internal/cgroups"
	"github.com/OpceanAI/Doki/pkg/common"
)

func TestResolvedDescriptorResolvesNamedVolumeWithoutMutatingConfig(t *testing.T) {
	cfg := &Config{Mounts: []common.Mount{
		{Type: common.MountVolume, Source: "db", Target: "/var/lib/data"},
		{Type: common.MountBind, Source: "/host/files", Target: "/files"},
	}}
	rt := NewRuntime(t.TempDir(), nil, WithVolumeResolver(fakeVolumeResolver{"db": "/host/volumes/db/_data"}))
	desc, err := rt.resolvedDescriptor(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(desc.Mounts) != 2 {
		t.Fatalf("mounts=%+v", desc.Mounts)
	}
	if desc.Mounts[0].Mount.Source != "db" || desc.Mounts[0].HostPath != "/host/volumes/db/_data" {
		t.Fatalf("volume mount=%+v", desc.Mounts[0])
	}
	if desc.Mounts[1].HostPath != "/host/files" {
		t.Fatalf("bind mount=%+v", desc.Mounts[1])
	}
	if cfg.Mounts[0].Source != "db" {
		t.Fatalf("config mutated: %+v", cfg.Mounts[0])
	}
}

func TestAndroidNativeStartUsesProviderProcess(t *testing.T) {
	provider := &fakeAndroidProvider{
		id:       "fake",
		match:    ProviderMatch{Matched: true, Required: true, Reason: "test"},
		prepared: &PreparedWorkload{Executable: "/bin/sh", Args: []string{"-c", "printf provider-started; exec sleep 30"}, Env: []string{"PATH=/usr/bin:/bin"}, Cwd: "/"},
	}
	reg := NewAndroidProviderRegistry()
	_ = reg.Register(provider)
	rt := NewRuntime(t.TempDir(), nil, WithAndroidProviderRegistry(reg))
	state, err := rt.Create(&Config{ID: "native-start", ImageRef: "example:1"})
	if err != nil {
		t.Fatal(err)
	}
	if state.Mode != ModeAndroidNative {
		t.Fatalf("mode=%v", state.Mode)
	}
	if err := rt.Start(state.ID); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rt.Kill(state.ID, 9) }()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		raw, _ := os.ReadFile(state.LogPath)
		if strings.Contains(string(raw), "provider-started") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	current, err := rt.State(state.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != common.StateRunning || current.Pid <= 0 {
		t.Fatalf("state=%+v", current)
	}
	raw, _ := os.ReadFile(state.LogPath)
	if !strings.Contains(string(raw), "provider-started") {
		t.Fatalf("log=%q", raw)
	}
	if provider.ensureCalls != 1 || provider.prepareCalls != 1 {
		t.Fatalf("ensure=%d prepare=%d", provider.ensureCalls, provider.prepareCalls)
	}
}

func TestAndroidNativeStartRejectsMissingProviderRelativeExecutableAndEnsureFailure(t *testing.T) {
	t.Run("missing provider", func(t *testing.T) {
		rt := NewRuntime(t.TempDir(), nil, WithAndroidProviderRegistry(NewAndroidProviderRegistry()))
		cfg := &Config{ID: "missing"}
		state := &ContainerState{ID: cfg.ID, Status: common.StateCreated, Created: time.Now(), Bundle: filepath.Join(rt.root, "bundles", cfg.ID), Config: cfg, Mode: ModeAndroidNative, LogPath: filepath.Join(rt.root, "containers", cfg.ID, "container.log")}
		if err := rt.saveState(state); err != nil {
			t.Fatal(err)
		}
		if err := rt.saveAndroidProviderState(cfg.ID, AndroidProviderState{Version: 1, ProviderID: "fake"}); err != nil {
			t.Fatal(err)
		}
		err := rt.Start(cfg.ID)
		if err == nil || !strings.Contains(err.Error(), `Android provider "fake" is not registered`) {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("relative executable", func(t *testing.T) {
		p := &fakeAndroidProvider{id: "fake", match: ProviderMatch{Matched: true, Required: true}, prepared: &PreparedWorkload{Executable: "bin/tool"}}
		reg := NewAndroidProviderRegistry()
		_ = reg.Register(p)
		rt := NewRuntime(t.TempDir(), nil, WithAndroidProviderRegistry(reg))
		state, err := rt.Create(&Config{ID: "relative", ImageRef: "x"})
		if err != nil {
			t.Fatal(err)
		}
		err = rt.Start(state.ID)
		if err == nil || !strings.Contains(err.Error(), "provider executable must be absolute") {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("ensure error", func(t *testing.T) {
		want := errors.New("cannot provision")
		p := &fakeAndroidProvider{id: "fake", match: ProviderMatch{Matched: true, Required: true}, ensureErr: want, prepared: &PreparedWorkload{Executable: "/bin/true"}}
		reg := NewAndroidProviderRegistry()
		_ = reg.Register(p)
		rt := NewRuntime(t.TempDir(), nil, WithAndroidProviderRegistry(reg))
		state, err := rt.Create(&Config{ID: "ensure", ImageRef: "x"})
		if err != nil {
			t.Fatal(err)
		}
		err = rt.Start(state.ID)
		if err == nil || !strings.Contains(err.Error(), want.Error()) {
			t.Fatalf("err=%v", err)
		}
		if p.prepareCalls != 0 {
			t.Fatalf("prepare called after ensure failure")
		}
	})
}

func newRunningAndroidProviderRuntime(t *testing.T, provider *fakeAndroidProvider) (*Runtime, *ContainerState) {
	t.Helper()
	reg := NewAndroidProviderRegistry()
	if err := reg.Register(provider); err != nil {
		t.Fatal(err)
	}
	rt := NewRuntime(t.TempDir(), nil, WithAndroidProviderRegistry(reg))
	state, err := rt.Create(&Config{ID: "exec-native", ImageRef: "example:1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.Start(state.ID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Kill(state.ID, 9) })
	return rt, state
}

func TestAndroidNativeExecUsesProvider(t *testing.T) {
	p := &fakeAndroidProvider{
		id: "fake", match: ProviderMatch{Matched: true, Required: true},
		prepared:     &PreparedWorkload{Executable: "/bin/sleep", Args: []string{"30"}, Env: []string{"PATH=/usr/bin:/bin"}, Cwd: "/"},
		preparedExec: &PreparedExec{Executable: "/bin/sh", Args: []string{"-c", "printf ready"}, Env: []string{"PATH=/usr/bin:/bin"}, Cwd: "/"},
	}
	rt, state := newRunningAndroidProviderRuntime(t, p)
	stdout, stderr, err := rt.Exec(state.ID, []string{"probe", "--ready"}, []string{"X=1"}, "/work", "user")
	if err != nil {
		t.Fatal(err)
	}
	if string(stdout) != "ready" || len(stderr) != 0 {
		t.Fatalf("stdout=%q stderr=%q", stdout, stderr)
	}
	if p.prepareExecCalls != 1 || p.lastExec == nil || len(p.lastExec.Args) != 2 || p.lastExec.Args[0] != "probe" || p.lastExec.Args[1] != "--ready" {
		t.Fatalf("prepareExecCalls=%d lastExec=%+v", p.prepareExecCalls, p.lastExec)
	}
}

func TestAndroidNativeExecAttachStreamsStdinAndStdout(t *testing.T) {
	p := &fakeAndroidProvider{
		id: "fake", match: ProviderMatch{Matched: true, Required: true},
		prepared:     &PreparedWorkload{Executable: "/bin/sleep", Args: []string{"30"}, Env: []string{"PATH=/usr/bin:/bin"}, Cwd: "/"},
		preparedExec: &PreparedExec{Executable: "/bin/sh", Args: []string{"-c", `read line; printf 'got:%s' "$line"`}, Env: []string{"PATH=/usr/bin:/bin"}, Cwd: "/"},
	}
	rt, state := newRunningAndroidProviderRuntime(t, p)
	res, err := rt.ExecAttach(state.ID, []string{"echo-through-provider"}, nil, "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := res.Stdin.Write([]byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	if err := res.Stdin.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(res.Stdout)
	if err != nil {
		t.Fatal(err)
	}
	if err := res.Wait(); err != nil {
		t.Fatal(err)
	}
	if string(out) != "got:hello" {
		t.Fatalf("stdout=%q", out)
	}
}

func TestAndroidNativeExecAttachUsesDedicatedProcessGroup(t *testing.T) {
	p := &fakeAndroidProvider{
		id: "fake", match: ProviderMatch{Matched: true, Required: true},
		prepared:     &PreparedWorkload{Executable: "/bin/sleep", Args: []string{"30"}, Env: []string{"PATH=/usr/bin:/bin"}, Cwd: "/"},
		preparedExec: &PreparedExec{Executable: "/bin/sleep", Args: []string{"30"}, Env: []string{"PATH=/usr/bin:/bin"}, Cwd: "/"},
	}
	rt, state := newRunningAndroidProviderRuntime(t, p)
	res, err := rt.ExecAttach(state.ID, []string{"sleep", "30"}, nil, "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(res.Pid, syscall.SIGKILL)
		_ = res.Wait()
	})

	gotPGID, err := syscall.Getpgid(res.Pid)
	if err != nil {
		t.Fatalf("Getpgid(%d): %v", res.Pid, err)
	}
	parentPGID := syscall.Getpgrp()
	if gotPGID == parentPGID {
		t.Fatalf("Android-native exec inherited daemon/test process group: pid=%d pgid=%d parent_pgid=%d", res.Pid, gotPGID, parentPGID)
	}
}

func TestAndroidNativeHealthcheckUsesProviderExec(t *testing.T) {
	p := &fakeAndroidProvider{
		id: "fake", match: ProviderMatch{Matched: true, Required: true},
		prepared:     &PreparedWorkload{Executable: "/bin/sleep", Args: []string{"30"}, Env: []string{"PATH=/usr/bin:/bin"}, Cwd: "/"},
		preparedExec: &PreparedExec{Executable: "/bin/sh", Args: []string{"-c", "printf healthy"}, Env: []string{"PATH=/usr/bin:/bin"}, Cwd: "/"},
	}
	rt, state := newRunningAndroidProviderRuntime(t, p)
	hc := NewHealthChecker(rt, state.ID, &HealthCheckConfig{Test: []string{"CMD", "provider-health"}, Timeout: time.Second})
	code, output := hc.runProbe([]string{"provider-health"}, time.Second)
	if code != 0 || output != "healthy" {
		t.Fatalf("code=%d output=%q", code, output)
	}
	if p.lastExec == nil || len(p.lastExec.Args) != 1 || p.lastExec.Args[0] != "provider-health" {
		t.Fatalf("lastExec=%+v", p.lastExec)
	}
}

func TestAndroidNativePauseUnpauseUsesPersistedPID(t *testing.T) {
	const missingPID = 1 << 30

	t.Run("pause", func(t *testing.T) {
		rt := NewRuntime(t.TempDir(), nil)
		rt.cgMgr = nil
		state := &ContainerState{ID: "pause-persisted", Status: common.StateRunning, Pid: missingPID, Mode: ModeAndroidNative}
		if err := rt.saveState(state); err != nil {
			t.Fatal(err)
		}
		if err := rt.Pause(state.ID); err == nil {
			t.Fatal("Pause succeeded without signaling persisted PID")
		}
		current, err := rt.State(state.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.Status != common.StateRunning {
			t.Fatalf("status=%s, want running after failed signal", current.Status)
		}
	})

	t.Run("cgroup freeze failure falls back to persisted pid", func(t *testing.T) {
		rt := NewRuntime(t.TempDir(), nil)
		rt.cgMgr = cgroups.NewManager(t.TempDir())
		if !rt.cgMgr.IsAvailable() {
			t.Skip("cgroup v2 not reported by kernel")
		}
		state := &ContainerState{ID: "pause-cgroup-fallback", Status: common.StateRunning, Pid: missingPID, Mode: ModeAndroidNative}
		if err := rt.saveState(state); err != nil {
			t.Fatal(err)
		}
		if err := rt.Pause(state.ID); err == nil {
			t.Fatal("Pause ignored cgroup freeze failure instead of signaling persisted PID")
		}
		current, err := rt.State(state.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.Status != common.StateRunning {
			t.Fatalf("status=%s, want running after failed fallback signal", current.Status)
		}
	})

	t.Run("unpause", func(t *testing.T) {
		rt := NewRuntime(t.TempDir(), nil)
		rt.cgMgr = nil
		state := &ContainerState{ID: "unpause-persisted", Status: common.StatePaused, Pid: missingPID, Mode: ModeAndroidNative}
		if err := rt.saveState(state); err != nil {
			t.Fatal(err)
		}
		if err := rt.Unpause(state.ID); err == nil {
			t.Fatal("Unpause succeeded without signaling persisted PID")
		}
		current, err := rt.State(state.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.Status != common.StatePaused {
			t.Fatalf("status=%s, want paused after failed signal", current.Status)
		}
	})
}

func TestAndroidNativeDeleteCallsProviderCleanup(t *testing.T) {
	t.Run("cleanup before removal", func(t *testing.T) {
		p := &fakeAndroidProvider{id: "fake", match: ProviderMatch{Matched: true, Required: true}}
		reg := NewAndroidProviderRegistry()
		if err := reg.Register(p); err != nil {
			t.Fatal(err)
		}
		rt := NewRuntime(t.TempDir(), nil, WithAndroidProviderRegistry(reg))
		state, err := rt.Create(&Config{ID: "cleanup-native", ImageRef: "example:1"})
		if err != nil {
			t.Fatal(err)
		}
		if err := rt.Delete(state.ID, false); err != nil {
			t.Fatal(err)
		}
		if p.cleanupCalls != 1 {
			t.Fatalf("cleanupCalls=%d, want 1", p.cleanupCalls)
		}
		if _, err := rt.State(state.ID); err == nil {
			t.Fatal("container state still exists after successful cleanup/delete")
		}
	})

	t.Run("cleanup failure retains state", func(t *testing.T) {
		cleanupErr := errors.New("cleanup failed")
		p := &fakeAndroidProvider{id: "fake", match: ProviderMatch{Matched: true, Required: true}, cleanupErr: cleanupErr}
		reg := NewAndroidProviderRegistry()
		if err := reg.Register(p); err != nil {
			t.Fatal(err)
		}
		rt := NewRuntime(t.TempDir(), nil, WithAndroidProviderRegistry(reg))
		state, err := rt.Create(&Config{ID: "cleanup-retry", ImageRef: "example:1"})
		if err != nil {
			t.Fatal(err)
		}
		err = rt.Delete(state.ID, false)
		if err == nil || !strings.Contains(err.Error(), cleanupErr.Error()) {
			t.Fatalf("Delete error=%v, want cleanup error", err)
		}
		if p.cleanupCalls != 1 {
			t.Fatalf("cleanupCalls=%d, want 1", p.cleanupCalls)
		}
		if _, err := rt.State(state.ID); err != nil {
			t.Fatalf("container state removed after cleanup failure: %v", err)
		}
	})
}

func TestAndroidNativeStopKillUsePersistedPID(t *testing.T) {
	newRuntime := func(t *testing.T, id string) (*Runtime, *fakeAndroidProvider, *ContainerState) {
		t.Helper()
		p := &fakeAndroidProvider{
			id:       "fake",
			match:    ProviderMatch{Matched: true, Required: true},
			prepared: &PreparedWorkload{Executable: "/bin/sleep", Args: []string{"30"}, Env: []string{"PATH=/usr/bin:/bin"}, Cwd: "/"},
		}
		reg := NewAndroidProviderRegistry()
		if err := reg.Register(p); err != nil {
			t.Fatal(err)
		}
		rt := NewRuntime(t.TempDir(), nil, WithAndroidProviderRegistry(reg))
		state, err := rt.Create(&Config{ID: id, ImageRef: "example:1"})
		if err != nil {
			t.Fatal(err)
		}
		if err := rt.Start(state.ID); err != nil {
			t.Fatal(err)
		}
		return rt, p, state
	}

	t.Run("stop", func(t *testing.T) {
		rt, _, state := newRuntime(t, "native-stop")
		if err := rt.Stop(state.ID, 1); err != nil {
			t.Fatal(err)
		}
		current, err := rt.State(state.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.Status != common.StateExited {
			t.Fatalf("status=%s, want exited", current.Status)
		}
		if current.Mode != ModeAndroidNative {
			t.Fatalf("mode=%v, want android-native", current.Mode)
		}
	})

	t.Run("kill", func(t *testing.T) {
		rt, _, state := newRuntime(t, "native-kill")
		if err := rt.Kill(state.ID, 9); err != nil {
			t.Fatal(err)
		}
		current, err := rt.State(state.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.Status != common.StateExited {
			t.Fatalf("status=%s, want exited", current.Status)
		}
		if current.Mode != ModeAndroidNative {
			t.Fatalf("mode=%v, want android-native", current.Mode)
		}
	})
}

func TestAndroidNativeRestartUsesPersistedProviderID(t *testing.T) {
	root := t.TempDir()
	persisted := &fakeAndroidProvider{
		id:       "persisted",
		match:    ProviderMatch{Matched: true, Required: true},
		prepared: &PreparedWorkload{Executable: "/bin/sleep", Args: []string{"30"}, Env: []string{"PATH=/usr/bin:/bin"}, Cwd: "/"},
	}
	reg1 := NewAndroidProviderRegistry()
	if err := reg1.Register(persisted); err != nil {
		t.Fatal(err)
	}
	rt1 := NewRuntime(root, nil, WithAndroidProviderRegistry(reg1))
	state, err := rt1.Create(&Config{ID: "native-restart", ImageRef: "example:1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := rt1.Start(state.ID); err != nil {
		t.Fatal(err)
	}
	if err := rt1.Stop(state.ID, 1); err != nil {
		t.Fatal(err)
	}

	other := &fakeAndroidProvider{
		id:       "other",
		match:    ProviderMatch{Matched: true, Required: true},
		prepared: &PreparedWorkload{Executable: "/bin/false", Env: []string{"PATH=/usr/bin:/bin"}, Cwd: "/"},
	}
	reg2 := NewAndroidProviderRegistry()
	if err := reg2.Register(other); err != nil {
		t.Fatal(err)
	}
	if err := reg2.Register(persisted); err != nil {
		t.Fatal(err)
	}
	rt2 := NewRuntime(root, nil, WithAndroidProviderRegistry(reg2))
	before, err := rt2.State(state.ID)
	if err != nil {
		t.Fatal(err)
	}
	if before.Status != common.StateExited || before.Mode != ModeAndroidNative {
		t.Fatalf("reloaded state=%+v", before)
	}
	ps, err := rt2.loadAndroidProviderState(state.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ps.ProviderID != "persisted" {
		t.Fatalf("provider ID=%q, want persisted", ps.ProviderID)
	}
	if err := rt2.Start(state.ID); err != nil {
		t.Fatalf("restart failed; provider may have been reselected: %v", err)
	}
	t.Cleanup(func() { _ = rt2.Kill(state.ID, 9) })
	if persisted.ensureCalls != 2 || persisted.prepareCalls != 2 {
		t.Fatalf("persisted ensure=%d prepare=%d, want 2/2", persisted.ensureCalls, persisted.prepareCalls)
	}
	if other.ensureCalls != 0 || other.prepareCalls != 0 {
		t.Fatalf("other provider was used: ensure=%d prepare=%d", other.ensureCalls, other.prepareCalls)
	}
	current, err := rt2.State(state.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != common.StateRunning || current.Mode != ModeAndroidNative {
		t.Fatalf("state after restart=%+v", current)
	}
}

func TestStopReleasesAndroidPortProxyBeforeReturning(t *testing.T) {
	provider := &fakeAndroidProvider{
		id:    "fake-stop-sync",
		match: ProviderMatch{Matched: true, Required: true},
	}
	reg := NewAndroidProviderRegistry()
	_ = reg.Register(provider)
	rt := NewRuntime(t.TempDir(), nil, WithAndroidProviderRegistry(reg))
	state, err := rt.Create(&Config{ID: "native-stop-sync", ImageRef: "example:1"})
	if err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("/bin/sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()
	reaped := make(chan struct{})
	go func() {
		_, _ = cmd.Process.Wait()
		close(reaped)
	}()

	state.Pid = cmd.Process.Pid
	state.Status = common.StateRunning
	state.Started = time.Now()
	if err := rt.saveState(state); err != nil {
		t.Fatal(err)
	}

	listenPort := freeTCPPort(t)
	forward, err := startTCPForward(PortForward{
		ListenHost: "127.0.0.1",
		ListenPort: listenPort,
		TargetHost: "127.0.0.1",
		TargetPort: 5432,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer forward.Close()
	rt.portProxyMu.Lock()
	rt.portProxies[state.ID] = []io.Closer{forward}
	rt.portProxyMu.Unlock()

	if err := rt.Stop(state.ID, 1); err != nil {
		t.Fatal(err)
	}
	select {
	case <-reaped:
	case <-time.After(time.Second):
		t.Fatal("provider process was not reaped after Stop")
	}

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", listenPort))
	if err != nil {
		t.Fatalf("Stop returned before releasing provider port proxy: %v", err)
	}
	_ = ln.Close()
}

func TestAndroidNativePortForwardLifecycle(t *testing.T) {
	t.Run("start and stop", func(t *testing.T) {
		target, echo := startEchoServer(t)
		defer echo.Close()
		targetHost, targetPortText, _ := net.SplitHostPort(target)
		targetPort, _ := strconv.Atoi(targetPortText)
		listenPort := freeTCPPort(t)
		provider := &fakeAndroidProvider{
			id:    "fake-port",
			match: ProviderMatch{Matched: true, Required: true},
			prepared: &PreparedWorkload{
				Executable: "/bin/sleep", Args: []string{"30"}, Env: []string{"PATH=/usr/bin:/bin"}, Cwd: "/",
				PortForwards: []PortForward{{ListenHost: "127.0.0.1", ListenPort: listenPort, TargetHost: targetHost, TargetPort: uint16(targetPort)}},
			},
		}
		reg := NewAndroidProviderRegistry()
		_ = reg.Register(provider)
		rt := NewRuntime(t.TempDir(), nil, WithAndroidProviderRegistry(reg))
		state, err := rt.Create(&Config{ID: "native-port-stop", ImageRef: "example:1"})
		if err != nil {
			t.Fatal(err)
		}
		if err := rt.Start(state.ID); err != nil {
			t.Fatal(err)
		}

		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", listenPort), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.Close()
		if err := rt.Stop(state.ID, 1); err != nil {
			t.Fatal(err)
		}

		deadline := time.Now().Add(2 * time.Second)
		for {
			ln, listenErr := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", listenPort))
			if listenErr == nil {
				_ = ln.Close()
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("proxy listener still owned after stop: %v", listenErr)
			}
			time.Sleep(20 * time.Millisecond)
		}
	})

	t.Run("natural exit closes proxy", func(t *testing.T) {
		target, echo := startEchoServer(t)
		defer echo.Close()
		targetHost, targetPortText, _ := net.SplitHostPort(target)
		targetPort, _ := strconv.Atoi(targetPortText)
		listenPort := freeTCPPort(t)
		provider := &fakeAndroidProvider{
			id:    "fake-port-exit",
			match: ProviderMatch{Matched: true, Required: true},
			prepared: &PreparedWorkload{
				Executable: "/bin/sleep", Args: []string{"0.15"}, Env: []string{"PATH=/usr/bin:/bin"}, Cwd: "/",
				PortForwards: []PortForward{{ListenHost: "127.0.0.1", ListenPort: listenPort, TargetHost: targetHost, TargetPort: uint16(targetPort)}},
			},
		}
		reg := NewAndroidProviderRegistry()
		_ = reg.Register(provider)
		rt := NewRuntime(t.TempDir(), nil, WithAndroidProviderRegistry(reg))
		state, err := rt.Create(&Config{ID: "native-port-exit", ImageRef: "example:1"})
		if err != nil {
			t.Fatal(err)
		}
		if err := rt.Start(state.ID); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(3 * time.Second)
		for {
			current, err := rt.State(state.ID)
			if err != nil {
				t.Fatal(err)
			}
			if current.Status == common.StateExited {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("container did not exit: %+v", current)
			}
			time.Sleep(20 * time.Millisecond)
		}
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", listenPort))
		if err != nil {
			t.Fatalf("proxy listener survived process exit: %v", err)
		}
		_ = ln.Close()
	})

	t.Run("listen failure rolls back start", func(t *testing.T) {
		listenPort := freeTCPPort(t)
		occupied, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", listenPort))
		if err != nil {
			t.Fatal(err)
		}
		defer occupied.Close()
		provider := &fakeAndroidProvider{
			id:    "fake-port-fail",
			match: ProviderMatch{Matched: true, Required: true},
			prepared: &PreparedWorkload{
				Executable: "/bin/sleep", Args: []string{"30"}, Env: []string{"PATH=/usr/bin:/bin"}, Cwd: "/",
				PortForwards: []PortForward{{ListenHost: "127.0.0.1", ListenPort: listenPort, TargetHost: "127.0.0.1", TargetPort: 5432}},
			},
		}
		reg := NewAndroidProviderRegistry()
		_ = reg.Register(provider)
		rt := NewRuntime(t.TempDir(), nil, WithAndroidProviderRegistry(reg))
		state, err := rt.Create(&Config{ID: "native-port-fail", ImageRef: "example:1"})
		if err != nil {
			t.Fatal(err)
		}
		err = rt.Start(state.ID)
		if err == nil || !strings.Contains(err.Error(), "TCP forward") {
			t.Fatalf("error=%v", err)
		}
		current, err := rt.State(state.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.Status != common.StateCreated || current.Pid != 0 {
			t.Fatalf("state after failed start=%+v", current)
		}
	})
}
