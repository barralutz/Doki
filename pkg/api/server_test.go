package api

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/OpceanAI/Doki/pkg/common"
	"github.com/OpceanAI/Doki/pkg/image"
	dokiruntime "github.com/OpceanAI/Doki/pkg/runtime"
)

func TestVolumeManagerSkipsBadMetadata(t *testing.T) {
	root := t.TempDir()
	badDir := filepath.Join(root, "bad")
	if err := os.MkdirAll(badDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badDir, "volume.json"), []byte("not-json"), 0644); err != nil {
		t.Fatal(err)
	}

	goodDir := filepath.Join(root, "good")
	if err := os.MkdirAll(goodDir, 0755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(common.VolumeInfo{Name: "good", Driver: "local", Mountpoint: goodDir})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(goodDir, "volume.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	vm, err := NewVolumeManager(root)
	if err != nil {
		t.Fatalf("NewVolumeManager() error = %v", err)
	}
	if _, err := vm.Get("good"); err != nil {
		t.Fatalf("expected good volume to load: %v", err)
	}
	if _, err := vm.Get("bad"); !common.IsNotFound(err) {
		t.Fatalf("bad metadata loaded unexpectedly: %v", err)
	}
}

func TestVolumeManagerCreateRemove(t *testing.T) {
	vm, err := NewVolumeManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	vol, err := vm.Create("data", "", nil, nil)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(vol.Mountpoint, "volume.json")); err != nil {
		t.Fatalf("volume metadata missing: %v", err)
	}
	if err := vm.Remove("data"); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if _, err := os.Stat(vol.Mountpoint); !os.IsNotExist(err) {
		t.Fatalf("volume directory still exists: %v", err)
	}
}

func TestRequestIDMiddlewareStoresContext(t *testing.T) {
	mw := NewMiddleware()
	seen := ""
	h := mw.RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = requestIDFromContext(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/_ping", nil)
	req.Header.Set("X-Request-ID", "req-123")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if seen != "req-123" {
		t.Fatalf("request ID in context = %q, want req-123", seen)
	}
	if got := rr.Header().Get("X-Request-ID"); got != "req-123" {
		t.Fatalf("response request ID = %q, want req-123", got)
	}
}

func TestContainerInspectCreatedIsRFC3339String(t *testing.T) {
	root := t.TempDir()
	rt := dokiruntime.NewRuntime(root, nil)
	created := time.Date(2026, 8, 20, 16, 3, 15, 123456789, time.UTC)
	state := &dokiruntime.ContainerState{
		ID:      "compose-inspect-test",
		Status:  common.StateCreated,
		Created: created,
		Config: &dokiruntime.Config{
			ImageRef: "alpine:latest",
			Annotations: map[string]string{
				"doki.name": "compose-inspect-test",
			},
		},
	}
	if err := rt.SaveState(state); err != nil {
		t.Fatalf("SaveState() error = %v", err)
	}

	s := &Server{runtime: rt}
	req := httptest.NewRequest(http.MethodGet, "/containers/compose-inspect-test/json", nil)
	rr := httptest.NewRecorder()
	s.handleContainerInspect(rr, req, state.ID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}

	var body map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode inspect response: %v", err)
	}
	got, ok := body["Created"].(string)
	if !ok {
		t.Fatalf("Created type = %T (%v), want RFC3339 string", body["Created"], body["Created"])
	}
	want := created.Format(time.RFC3339Nano)
	if got != want {
		t.Fatalf("Created = %q, want %q", got, want)
	}
}

func TestContainerInspectIncludesNetworkSettingsObject(t *testing.T) {
	root := t.TempDir()
	rt := dokiruntime.NewRuntime(root, nil)
	state := &dokiruntime.ContainerState{
		ID:      "compose-networksettings-test",
		Status:  common.StateCreated,
		Created: time.Now().UTC(),
		Config: &dokiruntime.Config{
			ImageRef:    "alpine:latest",
			NetworkMode: common.NetworkHost,
		},
	}
	if err := rt.SaveState(state); err != nil {
		t.Fatalf("SaveState() error = %v", err)
	}

	s := &Server{runtime: rt}
	req := httptest.NewRequest(http.MethodGet, "/containers/compose-networksettings-test/json", nil)
	rr := httptest.NewRecorder()
	s.handleContainerInspect(rr, req, state.ID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}

	var body map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode inspect response: %v", err)
	}
	ns, ok := body["NetworkSettings"].(map[string]interface{})
	if !ok {
		t.Fatalf("NetworkSettings type = %T (%v), want object", body["NetworkSettings"], body["NetworkSettings"])
	}
	if _, ok := ns["Networks"].(map[string]interface{}); !ok {
		t.Fatalf("NetworkSettings.Networks type = %T (%v), want object", ns["Networks"], ns["Networks"])
	}
}

func TestContainersListAllAcceptsDockerNumericBoolean(t *testing.T) {
	root := t.TempDir()
	rt := dokiruntime.NewRuntime(root, nil)
	state := &dokiruntime.ContainerState{
		ID:      "compose-created-list-test",
		Status:  common.StateCreated,
		Created: time.Now().UTC(),
		Config: &dokiruntime.Config{
			ImageRef: "alpine:latest",
			Labels: map[string]string{
				"com.docker.compose.project": "compose-test",
			},
		},
	}
	if err := rt.SaveState(state); err != nil {
		t.Fatalf("SaveState() error = %v", err)
	}

	s := &Server{runtime: rt}
	req := httptest.NewRequest(http.MethodGet, "/containers/json?all=1", nil)
	rr := httptest.NewRecorder()
	s.handleContainersList(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}

	var body []common.ContainerInfo
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode container list: %v", err)
	}
	if len(body) != 1 || body[0].ID != state.ID {
		t.Fatalf("containers = %#v, want created container %q when all=1", body, state.ID)
	}
}

func TestContainersListIncludesRuntimeConfigLabels(t *testing.T) {
	root := t.TempDir()
	rt := dokiruntime.NewRuntime(root, nil)
	state := &dokiruntime.ContainerState{
		ID:      "compose-label-list-test",
		Status:  common.StateCreated,
		Created: time.Now().UTC(),
		Config: &dokiruntime.Config{
			ImageRef: "alpine:latest",
			Labels: map[string]string{
				"com.docker.compose.project": "compose-test",
				"com.docker.compose.service": "web",
			},
		},
	}
	if err := rt.SaveState(state); err != nil {
		t.Fatalf("SaveState() error = %v", err)
	}

	s := &Server{runtime: rt}
	req := httptest.NewRequest(http.MethodGet, "/containers/json?all=1", nil)
	rr := httptest.NewRecorder()
	s.handleContainersList(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}

	var body []common.ContainerInfo
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode container list: %v", err)
	}
	if len(body) != 1 {
		t.Fatalf("container count = %d, want 1", len(body))
	}
	if got := body[0].Labels["com.docker.compose.service"]; got != "web" {
		t.Fatalf("compose service label = %q, want %q; labels=%v", got, "web", body[0].Labels)
	}
}

func TestImageDispatchSupportsRepositoryNamesWithSlash(t *testing.T) {
	store, err := image.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.SaveRecord(&image.ImageRecord{
		ID:           "sha256:minio-slash-test",
		RepoTags:     []string{"minio/minio:latest"},
		Created:      time.Now().Unix(),
		Architecture: "arm64",
		OS:           "linux",
	}); err != nil {
		t.Fatalf("SaveRecord: %v", err)
	}

	s := &Server{image: store}
	req := httptest.NewRequest(http.MethodGet, "/images/minio/minio:latest/json", nil)
	rr := httptest.NewRecorder()
	s.handleImageDispatch(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}

	var body map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode image inspect response: %v", err)
	}
	if got := body["RepoTags"]; got == nil {
		t.Fatalf("RepoTags missing from image inspect response: %v", body)
	}
}

func TestBuildAcceptsDockerTarContextBody(t *testing.T) {
	root := t.TempDir()
	store, err := image.NewStore(filepath.Join(root, "images"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	var body bytes.Buffer
	tw := tar.NewWriter(&body)
	dockerfile := []byte("FROM scratch\n")
	if err := tw.WriteHeader(&tar.Header{
		Name: "Dockerfile",
		Mode: 0644,
		Size: int64(len(dockerfile)),
	}); err != nil {
		t.Fatalf("WriteHeader: %v", err)
	}
	if _, err := tw.Write(dockerfile); err != nil {
		t.Fatalf("Write Dockerfile: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}

	s := &Server{
		config: &common.DokiConfig{DataDir: root},
		image:  store,
	}
	req := httptest.NewRequest(http.MethodPost,
		"/build?dockerfile=Dockerfile&t=docker-body-test:latest&buildargs=%7B%22FOO%22%3A%22bar%22%7D",
		bytes.NewReader(body.Bytes()),
	)
	rr := httptest.NewRecorder()
	s.handleBuild(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}
	if !store.Exists("docker-body-test:latest") {
		t.Fatal("built image docker-body-test:latest was not persisted")
	}
}
