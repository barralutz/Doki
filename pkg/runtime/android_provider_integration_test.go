package runtime

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"

	"github.com/OpceanAI/Doki/pkg/common"
)

func writeAndroidProviderIntegrationLayer(t *testing.T, path string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	body := []byte("legacy-rootfs-ok\n")
	if err := tw.WriteHeader(&tar.Header{Name: "probe.txt", Mode: 0644, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestAndroidProviderIntegrationLifecycleSurvivesRuntimeReload(t *testing.T) {
	root := t.TempDir()
	volumeHostPath := filepath.Join(root, "volumes", "probe", "_data")
	if err := os.MkdirAll(volumeHostPath, 0755); err != nil {
		t.Fatal(err)
	}
	resolver := fakeVolumeResolver{"probe": volumeHostPath}
	provider := &fakeAndroidProvider{
		id:    "fake",
		match: ProviderMatch{Matched: true, Required: true, Reason: "integration"},
		prepared: &PreparedWorkload{
			Executable: "/bin/sh",
			Args:       []string{"-c", "printf integration-started; exec sleep 30"},
			Env:        []string{"PATH=/usr/bin:/bin"},
			Cwd:        "/",
		},
		preparedExec: &PreparedExec{
			Executable: "/bin/sh",
			Args:       []string{"-c", "printf integration-exec"},
			Env:        []string{"PATH=/usr/bin:/bin"},
			Cwd:        "/",
		},
	}
	reg := NewAndroidProviderRegistry()
	if err := reg.Register(provider); err != nil {
		t.Fatal(err)
	}

	rt1 := NewRuntime(root, nil, WithAndroidProviderRegistry(reg), WithVolumeResolver(resolver))
	state, err := rt1.Create(&Config{
		ID:       "provider-integration",
		ImageRef: "example/android-provider:1",
		Mounts: []common.Mount{{
			Type:   common.MountVolume,
			Source: "probe",
			Target: "/var/lib/probe",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if state.Mode != ModeAndroidNative {
		t.Fatalf("mode=%v, want android-native", state.Mode)
	}
	ps, err := rt1.loadAndroidProviderState(state.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ps.ProviderID != "fake" {
		t.Fatalf("provider ID=%q, want fake", ps.ProviderID)
	}
	if err := rt1.Start(state.ID); err != nil {
		t.Fatal(err)
	}
	started, err := rt1.State(state.ID)
	if err != nil {
		t.Fatal(err)
	}
	if started.Status != common.StateRunning || started.Pid <= 0 {
		t.Fatalf("started state=%+v", started)
	}
	if provider.lastDescriptor.Mounts[0].Mount.Source != "probe" || provider.lastDescriptor.Mounts[0].HostPath != volumeHostPath {
		t.Fatalf("provider mount=%+v", provider.lastDescriptor.Mounts[0])
	}
	if started.Config.Mounts[0].Source != "probe" {
		t.Fatalf("persisted mount source=%q, want logical probe", started.Config.Mounts[0].Source)
	}
	logData, err := os.ReadFile(started.LogPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(logData) != "integration-started" {
		t.Fatalf("log=%q", logData)
	}
	stdout, stderr, err := rt1.Exec(state.ID, []string{"probe"}, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if string(stdout) != "integration-exec" || len(stderr) != 0 {
		t.Fatalf("stdout=%q stderr=%q", stdout, stderr)
	}
	if err := rt1.Stop(state.ID, 1); err != nil {
		t.Fatal(err)
	}
	stopped, err := rt1.State(state.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stopped.Status != common.StateExited || stopped.Mode != ModeAndroidNative {
		t.Fatalf("stopped state=%+v", stopped)
	}

	rt2 := NewRuntime(root, nil, WithAndroidProviderRegistry(reg), WithVolumeResolver(resolver))
	reloaded, err := rt2.State(state.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Mode != ModeAndroidNative {
		t.Fatalf("reloaded mode=%v", reloaded.Mode)
	}
	if err := rt2.Start(state.ID); err != nil {
		t.Fatal(err)
	}
	if provider.ensureCalls != 2 || provider.prepareCalls != 2 {
		t.Fatalf("ensure=%d prepare=%d, want 2/2", provider.ensureCalls, provider.prepareCalls)
	}
	ps, err = rt2.loadAndroidProviderState(state.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ps.ProviderID != "fake" {
		t.Fatalf("provider after reload=%q, want fake", ps.ProviderID)
	}
	if err := rt2.Delete(state.ID, true); err != nil {
		t.Fatal(err)
	}
	if provider.cleanupCalls != 1 {
		t.Fatalf("cleanupCalls=%d, want 1", provider.cleanupCalls)
	}
	if _, err := rt2.State(state.ID); err == nil {
		t.Fatal("container state still exists after delete")
	}
	if _, err := os.Stat(filepath.Join(root, "containers", state.ID)); !os.IsNotExist(err) {
		t.Fatalf("container directory still exists: %v", err)
	}
}

func TestAndroidProviderIntegrationNoProviderKeepsLegacyExtraction(t *testing.T) {
	root := t.TempDir()
	layer := filepath.Join(root, "layer.tar.gz")
	writeAndroidProviderIntegrationLayer(t, layer)
	runtimeRoot := filepath.Join(root, "runtime")
	rt := NewRuntime(runtimeRoot, nil, WithAndroidProviderRegistry(NewAndroidProviderRegistry()))
	state, err := rt.Create(&Config{
		ID:          "legacy-integration",
		ImageRef:    "example/legacy:1",
		ImageLayers: []string{layer},
	})
	if err != nil {
		t.Fatal(err)
	}
	if state.Mode != rt.Mode() {
		t.Fatalf("mode=%v legacy=%v", state.Mode, rt.Mode())
	}
	if state.Config.RootfsReady == "" {
		t.Fatal("legacy rootfs was not prepared")
	}
	data, err := os.ReadFile(filepath.Join(state.Config.RootfsReady, "probe.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "legacy-rootfs-ok\n" {
		t.Fatalf("probe=%q", data)
	}
	if _, err := os.Stat(rt.androidProviderStatePath(state.ID)); !os.IsNotExist(err) {
		t.Fatalf("android-provider.json exists for legacy workload: %v", err)
	}
}
