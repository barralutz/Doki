package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OpceanAI/Doki/pkg/common"
)

type fakeVolumeResolver map[string]string

func (f fakeVolumeResolver) Resolve(name string) (string, error) {
	p, ok := f[name]
	if !ok {
		return "", common.NewErrNotFound("volume", name)
	}
	return p, nil
}

func TestRuntimeResolveMountPreservesLogicalSource(t *testing.T) {
	logical := common.Mount{Type: common.MountVolume, Source: "db", Target: "/data"}
	rt := NewRuntime(t.TempDir(), nil, WithVolumeResolver(fakeVolumeResolver{"db": "/host/volumes/db/_data"}))

	resolved, err := rt.ResolveMount(logical)
	if err != nil {
		t.Fatal(err)
	}
	if logical.Source != "db" || logical.Type != common.MountVolume {
		t.Fatalf("input mutated: %#v", logical)
	}
	if resolved.Source != "/host/volumes/db/_data" {
		t.Fatalf("resolved source = %q", resolved.Source)
	}
	if resolved.Type != common.MountBind {
		t.Fatalf("resolved type = %q, want bind", resolved.Type)
	}
	if resolved.Target != "/data" {
		t.Fatalf("resolved target = %q, want /data", resolved.Target)
	}
}

func TestRuntimeResolveMountPassesThroughNonVolume(t *testing.T) {
	rt := NewRuntime(t.TempDir(), nil)
	logical := common.Mount{Type: common.MountBind, Source: "/host/data", Target: "/data", ReadOnly: true}
	resolved, err := rt.ResolveMount(logical)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != logical {
		t.Fatalf("resolved = %#v, want %#v", resolved, logical)
	}
}

func TestRuntimeResolveMountRejectsMissingResolver(t *testing.T) {
	rt := NewRuntime(t.TempDir(), nil)
	_, err := rt.ResolveMount(common.Mount{Type: common.MountVolume, Source: "db", Target: "/data"})
	if err == nil || !strings.Contains(err.Error(), `named volume "db": no volume resolver configured`) {
		t.Fatalf("error = %v", err)
	}
}

func TestRuntimeResolveMountReportsUnknownVolume(t *testing.T) {
	rt := NewRuntime(t.TempDir(), nil, WithVolumeResolver(fakeVolumeResolver{}))
	_, err := rt.ResolveMount(common.Mount{Type: common.MountVolume, Source: "missing", Target: "/data"})
	if err == nil || !strings.Contains(err.Error(), `resolve named volume "missing"`) {
		t.Fatalf("error = %v", err)
	}
}

func TestAppendProotMountArgsResolvesNamedVolume(t *testing.T) {
	rootfs := t.TempDir()
	logical := common.Mount{Type: common.MountVolume, Source: "db", Target: "/var/lib/data"}
	rt := NewRuntime(t.TempDir(), nil, WithVolumeResolver(fakeVolumeResolver{"db": "/host/volumes/db/_data"}))

	got, err := rt.appendProotMountArgs([]string{"--base"}, rootfs, []common.Mount{logical})
	if err != nil {
		t.Fatal(err)
	}
	wantPair := []string{"-b", "/host/volumes/db/_data:/var/lib/data"}
	found := false
	for i := 0; i+1 < len(got); i++ {
		if got[i] == wantPair[0] && got[i+1] == wantPair[1] {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("proot args = %#v, want pair %#v", got, wantPair)
	}
	if logical.Source != "db" || logical.Type != common.MountVolume {
		t.Fatalf("logical mount mutated: %#v", logical)
	}
	if st, err := os.Stat(filepath.Join(rootfs, "var", "lib", "data")); err != nil || !st.IsDir() {
		t.Fatalf("guest target not created: stat=%v err=%v", st, err)
	}
}

func TestAppendProotMountArgsRejectsMissingNamedVolume(t *testing.T) {
	rt := NewRuntime(t.TempDir(), nil, WithVolumeResolver(fakeVolumeResolver{}))
	_, err := rt.appendProotMountArgs(nil, t.TempDir(), []common.Mount{{Type: common.MountVolume, Source: "missing", Target: "/data"}})
	if err == nil || !strings.Contains(err.Error(), `resolve named volume "missing"`) {
		t.Fatalf("error = %v", err)
	}
}

func TestSetupMountsRejectsMissingNamedVolume(t *testing.T) {
	rt := NewRuntime(t.TempDir(), nil, WithVolumeResolver(fakeVolumeResolver{}))
	err := rt.setupMounts(t.TempDir(), &Config{Mounts: []common.Mount{{Type: common.MountVolume, Source: "missing", Target: "/data"}}})
	if err == nil || !strings.Contains(err.Error(), `resolve named volume "missing"`) {
		t.Fatalf("setupMounts error = %v, want missing named-volume resolution error", err)
	}
}

func TestPrepareNamedVolumesSeedsEmptyVolumeFromImage(t *testing.T) {
	rootfs := t.TempDir()
	target := filepath.Join(rootfs, "etc", "demo")
	if err := os.MkdirAll(target, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "default.conf"), []byte("seeded\n"), 0640); err != nil {
		t.Fatal(err)
	}

	volumeData := filepath.Join(t.TempDir(), "db", "_data")
	if err := os.MkdirAll(volumeData, 0755); err != nil {
		t.Fatal(err)
	}
	rt := NewRuntime(t.TempDir(), nil, WithVolumeResolver(fakeVolumeResolver{"db": volumeData}))
	mounts := []common.Mount{{Type: common.MountVolume, Source: "db", Target: "/etc/demo"}}

	if err := rt.prepareNamedVolumes(rootfs, mounts); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(volumeData, "default.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "seeded\n" {
		t.Fatalf("seeded content = %q", got)
	}
	st, err := os.Stat(filepath.Join(volumeData, "default.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0640 {
		t.Fatalf("seeded mode = %o, want 640", st.Mode().Perm())
	}
}

func TestPrepareNamedVolumesHonorsNoCopy(t *testing.T) {
	rootfs := t.TempDir()
	target := filepath.Join(rootfs, "data")
	if err := os.MkdirAll(target, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "seed.txt"), []byte("seed"), 0644); err != nil {
		t.Fatal(err)
	}
	volumeData := filepath.Join(t.TempDir(), "db", "_data")
	if err := os.MkdirAll(volumeData, 0755); err != nil {
		t.Fatal(err)
	}
	rt := NewRuntime(t.TempDir(), nil, WithVolumeResolver(fakeVolumeResolver{"db": volumeData}))
	mounts := []common.Mount{{
		Type:          common.MountVolume,
		Source:        "db",
		Target:        "/data",
		VolumeOptions: &common.VolumeOptions{NoCopy: true},
	}}

	if err := rt.prepareNamedVolumes(rootfs, mounts); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(volumeData)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("NoCopy volume entries = %v, want empty", entries)
	}
}

func TestPrepareNamedVolumesDoesNotSeedNonEmptyVolume(t *testing.T) {
	rootfs := t.TempDir()
	target := filepath.Join(rootfs, "data")
	if err := os.MkdirAll(target, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "seed.txt"), []byte("image"), 0644); err != nil {
		t.Fatal(err)
	}
	volumeData := filepath.Join(t.TempDir(), "db", "_data")
	if err := os.MkdirAll(volumeData, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(volumeData, "existing.txt"), []byte("persisted"), 0644); err != nil {
		t.Fatal(err)
	}
	rt := NewRuntime(t.TempDir(), nil, WithVolumeResolver(fakeVolumeResolver{"db": volumeData}))

	if err := rt.prepareNamedVolumes(rootfs, []common.Mount{{Type: common.MountVolume, Source: "db", Target: "/data"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(volumeData, "seed.txt")); !os.IsNotExist(err) {
		t.Fatalf("seed.txt was copied into non-empty volume: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(volumeData, "existing.txt"))
	if err != nil || string(got) != "persisted" {
		t.Fatalf("existing data changed: %q err=%v", got, err)
	}
}

func TestBuildProotExecArgsIncludesNamedVolume(t *testing.T) {
	rootfs := t.TempDir()
	rt := NewRuntime(t.TempDir(), nil, WithVolumeResolver(fakeVolumeResolver{"db": "/host/volumes/db/_data"}))
	mounts := []common.Mount{{Type: common.MountVolume, Source: "db", Target: "/data"}}

	got, err := rt.buildProotExecArgs(rootfs, mounts, "", "", []string{"cat", "/data/probe.txt"})
	if err != nil {
		t.Fatal(err)
	}
	wantMount := "/host/volumes/db/_data:/data"
	foundMount := false
	foundWD := false
	for i := 0; i+1 < len(got); i++ {
		if got[i] == "-b" && got[i+1] == wantMount {
			foundMount = true
		}
		if got[i] == "-w" && got[i+1] == "/" {
			foundWD = true
		}
	}
	if !foundMount {
		t.Fatalf("exec args = %#v, missing named-volume bind %q", got, wantMount)
	}
	if !foundWD {
		t.Fatalf("exec args = %#v, missing default guest cwd /", got)
	}
	if len(got) < 2 || got[len(got)-2] != "cat" || got[len(got)-1] != "/data/probe.txt" {
		t.Fatalf("exec command tail = %#v", got)
	}
}

func TestBuildProotExecArgsRejectsMissingNamedVolume(t *testing.T) {
	rt := NewRuntime(t.TempDir(), nil, WithVolumeResolver(fakeVolumeResolver{}))
	_, err := rt.buildProotExecArgs(t.TempDir(), []common.Mount{{Type: common.MountVolume, Source: "missing", Target: "/data"}}, "", "", []string{"true"})
	if err == nil || !strings.Contains(err.Error(), `resolve named volume "missing"`) {
		t.Fatalf("error = %v", err)
	}
}
