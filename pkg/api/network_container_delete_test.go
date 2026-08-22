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
	"github.com/OpceanAI/Doki/pkg/network"
	dokiruntime "github.com/OpceanAI/Doki/pkg/runtime"
	"github.com/OpceanAI/Doki/pkg/volume"
)

func TestContainerDeleteDisconnectsNetworkEndpoints(t *testing.T) {
	root := t.TempDir()
	store, err := image.NewStore(filepath.Join(root, "images"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRecord(&image.ImageRecord{
		ID:           "sha256:network-delete-integration",
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

	nw, err := netMgr.CreateNetwork(&network.NetworkConfig{Name: "compose_default", Driver: "bridge"})
	if err != nil {
		t.Fatal(err)
	}
	createReq := httptest.NewRequest(http.MethodPost, "/containers/create?name=demo&pull=never", strings.NewReader(`{"Image":"alpine:latest"}`))
	createRR := httptest.NewRecorder()
	s.ServeHTTP(createRR, createReq)
	if createRR.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", createRR.Code, createRR.Body.String())
	}
	var created struct {
		ID string `json:"Id"`
	}
	if err := json.Unmarshal(createRR.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if err := netMgr.Connect(nw.ID, created.ID, "", nil, nil, 0); err != nil {
		t.Fatal(err)
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/containers/"+created.ID+"?force=true", nil)
	deleteRR := httptest.NewRecorder()
	s.ServeHTTP(deleteRR, deleteReq)
	if deleteRR.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d body=%s", deleteRR.Code, deleteRR.Body.String())
	}
	if err := netMgr.RemoveNetwork(nw.ID); err != nil {
		t.Fatalf("container delete left network endpoint behind: %v", err)
	}
}
