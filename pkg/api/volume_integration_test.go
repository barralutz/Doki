package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OpceanAI/Doki/pkg/common"
	"github.com/OpceanAI/Doki/pkg/image"
	"github.com/OpceanAI/Doki/pkg/network"
	dokiruntime "github.com/OpceanAI/Doki/pkg/runtime"
	"github.com/OpceanAI/Doki/pkg/volume"
)

func TestDockerAPINamedVolumeLifecycle(t *testing.T) {
	root := t.TempDir()
	store, err := image.NewStore(filepath.Join(root, "images"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRecord(&image.ImageRecord{
		ID:           "sha256:volume-integration",
		RepoTags:     []string{"alpine:latest"},
		Created:      time.Now().Unix(),
		Architecture: "arm64",
		OS:           "linux",
		Config:       &image.Config{Config: image.ImageConfig{Cmd: []string{"sh"}}},
	}); err != nil {
		t.Fatal(err)
	}

	vm, err := volume.NewManager(filepath.Join(root, "volumes"))
	if err != nil {
		t.Fatal(err)
	}
	rt := dokiruntime.NewRuntime(filepath.Join(root, "runtime"), nil, dokiruntime.WithVolumeResolver(vm))
	netMgr, err := network.NewManager(
		filepath.Join(root, "networks"),
		network.NewFirewallManager(network.DetectFirewallBackend()),
		network.NewDNSServer(nil),
	)
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewServer(&common.DokiConfig{DataDir: root}, rt, store, netMgr, vm)
	if err != nil {
		t.Fatal(err)
	}

	createBody := `{"Image":"alpine:latest","HostConfig":{"Binds":["demo_data:/data:rw"]}}`
	createReq := httptest.NewRequest(http.MethodPost, "/containers/create?name=demo&pull=never", strings.NewReader(createBody))
	createRR := httptest.NewRecorder()
	s.ServeHTTP(createRR, createReq)
	if createRR.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want %d; body=%s", createRR.Code, http.StatusCreated, createRR.Body.String())
	}
	var created struct {
		ID string `json:"Id"`
	}
	if err := json.Unmarshal(createRR.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	state, err := rt.State(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Config.Mounts) != 1 || state.Config.Mounts[0].Type != common.MountVolume || state.Config.Mounts[0].Source != "demo_data" {
		t.Fatalf("persisted mounts = %#v", state.Config.Mounts)
	}

	v, err := vm.Get("demo_data")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(v.Mountpoint, "persist.txt"), []byte("kept"), 0644); err != nil {
		t.Fatal(err)
	}

	reloaded, err := volume.NewManager(filepath.Join(root, "volumes"))
	if err != nil {
		t.Fatal(err)
	}
	reloadedPath, err := reloaded.Resolve("demo_data")
	if err != nil {
		t.Fatal(err)
	}
	if reloadedPath != v.Mountpoint {
		t.Fatalf("reloaded mountpoint = %q, want %q", reloadedPath, v.Mountpoint)
	}
	if got, err := os.ReadFile(filepath.Join(reloadedPath, "persist.txt")); err != nil || string(got) != "kept" {
		t.Fatalf("reloaded payload = %q err=%v", got, err)
	}

	deleteContainerReq := httptest.NewRequest(http.MethodDelete, "/containers/"+created.ID+"?force=true", nil)
	deleteContainerRR := httptest.NewRecorder()
	s.ServeHTTP(deleteContainerRR, deleteContainerReq)
	if deleteContainerRR.Code != http.StatusNoContent {
		t.Fatalf("container delete status = %d, want %d; body=%s", deleteContainerRR.Code, http.StatusNoContent, deleteContainerRR.Body.String())
	}

	deleteVolumeReq := httptest.NewRequest(http.MethodDelete, "/volumes/demo_data", nil)
	deleteVolumeRR := httptest.NewRecorder()
	s.ServeHTTP(deleteVolumeRR, deleteVolumeReq)
	if deleteVolumeRR.Code != http.StatusNoContent {
		t.Fatalf("volume delete status = %d, want %d; body=%s", deleteVolumeRR.Code, http.StatusNoContent, deleteVolumeRR.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, "volumes", "demo_data")); !os.IsNotExist(err) {
		t.Fatalf("volume directory still exists after delete: %v", err)
	}
}
