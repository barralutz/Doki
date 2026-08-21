package runtime

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
		prepared: &PreparedWorkload{Executable: "/bin/sh", Args: []string{"-c", "printf provider-started; sleep 30"}, Env: []string{"PATH=/usr/bin:/bin"}, Cwd: "/"},
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
