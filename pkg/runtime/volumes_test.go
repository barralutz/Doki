package runtime

import (
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
