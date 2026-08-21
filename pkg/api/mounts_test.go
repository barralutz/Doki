package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OpceanAI/Doki/pkg/common"
	"github.com/OpceanAI/Doki/pkg/image"
	dokiruntime "github.com/OpceanAI/Doki/pkg/runtime"
	"github.com/OpceanAI/Doki/pkg/volume"
)

func TestParseHostConfigBindClassifiesNamedVolumeAndBind(t *testing.T) {
	tests := []struct {
		name string
		spec string
		want common.Mount
	}{
		{
			name: "compose named volume rw",
			spec: "mipctemuco_postgres_data:/var/lib/postgresql/data:rw",
			want: common.Mount{Type: common.MountVolume, Source: "mipctemuco_postgres_data", Target: "/var/lib/postgresql/data"},
		},
		{
			name: "named volume ro",
			spec: "cache-data:/cache:ro",
			want: common.Mount{Type: common.MountVolume, Source: "cache-data", Target: "/cache", ReadOnly: true},
		},
		{
			name: "absolute bind",
			spec: "/data/local/files:/files:rw",
			want: common.Mount{Type: common.MountBind, Source: "/data/local/files", Target: "/files"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseHostConfigBind(tt.spec)
			if err != nil {
				t.Fatalf("parseHostConfigBind(%q) error = %v", tt.spec, err)
			}
			if got.Type != tt.want.Type || got.Source != tt.want.Source || got.Target != tt.want.Target || got.ReadOnly != tt.want.ReadOnly {
				t.Fatalf("parseHostConfigBind(%q) = %#v, want %#v", tt.spec, got, tt.want)
			}
		})
	}
}

func TestParseHostConfigBindRejectsInvalidSpecs(t *testing.T) {
	for _, spec := range []string{
		"",
		":/data:rw",
		"data::rw",
		"../bad:/data:rw",
		"data:relative:rw",
		"data:/data:shared",
	} {
		t.Run(spec, func(t *testing.T) {
			if _, err := parseHostConfigBind(spec); err == nil {
				t.Fatalf("parseHostConfigBind(%q) unexpectedly succeeded", spec)
			}
		})
	}
}

func TestContainerCreateAutoCreatesNamedVolumeFromHostConfigBinds(t *testing.T) {
	root := t.TempDir()
	store, err := image.NewStore(filepath.Join(root, "images"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRecord(&image.ImageRecord{
		ID:           "sha256:named-volume-test",
		RepoTags:     []string{"alpine:latest"},
		Created:      time.Now().Unix(),
		Architecture: "arm64",
		OS:           "linux",
		Config: &image.Config{Config: image.ImageConfig{
			Cmd: []string{"sh"},
		}},
	}); err != nil {
		t.Fatal(err)
	}

	rt := dokiruntime.NewRuntime(filepath.Join(root, "runtime"), nil)
	vm, err := volume.NewManager(filepath.Join(root, "volumes"))
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{runtime: rt, image: store, volumes: vm}

	body := `{"Image":"alpine:latest","HostConfig":{"Binds":["demo_data:/var/lib/demo:rw"]}}`
	req := httptest.NewRequest(http.MethodPost, "/containers/create?name=demo&pull=never", strings.NewReader(body))
	rr := httptest.NewRecorder()
	s.handleContainerCreate(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusCreated, rr.Body.String())
	}

	var created struct {
		ID string `json:"Id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	state, err := rt.State(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Config.Mounts) != 1 {
		t.Fatalf("mounts = %#v, want exactly one", state.Config.Mounts)
	}
	got := state.Config.Mounts[0]
	if got.Type != common.MountVolume || got.Source != "demo_data" || got.Target != "/var/lib/demo" || got.ReadOnly {
		t.Fatalf("mount = %#v, want named volume demo_data:/var/lib/demo:rw", got)
	}
	if _, err := vm.Get("demo_data"); err != nil {
		t.Fatalf("named volume was not auto-created: %v", err)
	}
}

func TestNewServerUsesInjectedVolumeManager(t *testing.T) {
	root := t.TempDir()
	store, err := image.NewStore(filepath.Join(root, "images"))
	if err != nil {
		t.Fatal(err)
	}
	rt := dokiruntime.NewRuntime(filepath.Join(root, "runtime"), nil)
	vm, err := volume.NewManager(filepath.Join(root, "volumes"))
	if err != nil {
		t.Fatal(err)
	}

	s, err := NewServer(&common.DokiConfig{DataDir: root}, rt, store, nil, vm)
	if err != nil {
		t.Fatal(err)
	}
	if s.volumes != vm {
		t.Fatal("NewServer did not retain the injected volume manager instance")
	}
}

func newExitedContainerReferencingVolume(t *testing.T, rt *dokiruntime.Runtime, name string) {
	t.Helper()
	state := &dokiruntime.ContainerState{
		ID:      "exited-volume-ref",
		Status:  common.StateExited,
		Created: time.Now().UTC(),
		Config: &dokiruntime.Config{Mounts: []common.Mount{{
			Type:   common.MountVolume,
			Source: name,
			Target: "/data",
		}}},
	}
	if err := rt.SaveState(state); err != nil {
		t.Fatal(err)
	}
}

func TestVolumesPrunePreservesVolumeReferencedByExitedContainer(t *testing.T) {
	root := t.TempDir()
	rt := dokiruntime.NewRuntime(filepath.Join(root, "runtime"), nil)
	vm, err := volume.NewManager(filepath.Join(root, "volumes"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := vm.Create("db", "local", nil, nil); err != nil {
		t.Fatal(err)
	}
	newExitedContainerReferencingVolume(t, rt, "db")
	s := &Server{runtime: rt, volumes: vm}

	req := httptest.NewRequest(http.MethodPost, "/volumes/prune", nil)
	rr := httptest.NewRecorder()
	s.handleVolumesPrune(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}
	if _, err := vm.Get("db"); err != nil {
		t.Fatalf("prune deleted volume referenced by exited container: %v", err)
	}
}

func TestVolumeDeleteForceRejectsReferencedVolume(t *testing.T) {
	root := t.TempDir()
	rt := dokiruntime.NewRuntime(filepath.Join(root, "runtime"), nil)
	vm, err := volume.NewManager(filepath.Join(root, "volumes"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := vm.Create("db", "local", nil, nil); err != nil {
		t.Fatal(err)
	}
	newExitedContainerReferencingVolume(t, rt, "db")
	s := &Server{runtime: rt, volumes: vm}

	req := httptest.NewRequest(http.MethodDelete, "/volumes/db?force=true", nil)
	rr := httptest.NewRecorder()
	s.handleVolumeDispatch(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusConflict, rr.Body.String())
	}
	if _, err := vm.Get("db"); err != nil {
		t.Fatalf("force delete removed referenced volume: %v", err)
	}
}
