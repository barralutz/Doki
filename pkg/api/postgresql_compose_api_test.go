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
)

func newComposeAPITestServer(t *testing.T) (*Server, *dokiruntime.Runtime) {
	t.Helper()
	root := t.TempDir()
	store, err := image.NewStore(filepath.Join(root, "images"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRecord(&image.ImageRecord{
		ID:           "sha256:compose-api-test",
		RepoTags:     []string{"postgres:16-alpine"},
		Created:      time.Now().Unix(),
		Architecture: "arm64",
		OS:           "linux",
		Config: &image.Config{Config: image.ImageConfig{
			Cmd: []string{"postgres"},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	rt := dokiruntime.NewRuntime(filepath.Join(root, "runtime"), nil)
	return &Server{runtime: rt, image: store}, rt
}

func TestContainerCreatePreservesRequestedHealthcheck(t *testing.T) {
	s, rt := newComposeAPITestServer(t)
	body := `{
		"Image":"postgres:16-alpine",
		"Healthcheck":{
			"Test":["CMD-SHELL","pg_isready -U techservice -d techservice"],
			"Interval":2000000000,
			"Timeout":2000000000,
			"Retries":20,
			"StartPeriod":2000000000
		}
	}`
	req := httptest.NewRequest(http.MethodPost, "/containers/create?name=pg-health&pull=never", strings.NewReader(body))
	rr := httptest.NewRecorder()
	s.handleContainerCreate(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
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
	hc := state.Config.HealthCheck
	if hc == nil {
		t.Fatal("requested Docker Healthcheck was dropped during create")
	}
	if got := strings.Join(hc.Test, " "); got != "CMD-SHELL pg_isready -U techservice -d techservice" {
		t.Fatalf("healthcheck test=%q", got)
	}
	if hc.Interval != 2*time.Second || hc.Timeout != 2*time.Second || hc.Retries != 20 || hc.StartPeriod != 2*time.Second {
		t.Fatalf("healthcheck=%+v", hc)
	}
}

func TestContainerInspectIncludesHealthStatus(t *testing.T) {
	rt := dokiruntime.NewRuntime(t.TempDir(), nil)
	state := &dokiruntime.ContainerState{
		ID:      "pg-inspect-health",
		Status:  "running",
		Created: time.Now().UTC(),
		Config:  &dokiruntime.Config{ImageRef: "postgres:16-alpine"},
		HealthStatus: &common.HealthStatus{
			Status:        "healthy",
			FailingStreak: 0,
			Log:           []common.HealthCheckResult{},
		},
	}
	if err := rt.SaveState(state); err != nil {
		t.Fatal(err)
	}
	s := &Server{runtime: rt}
	req := httptest.NewRequest(http.MethodGet, "/containers/pg-inspect-health/json", nil)
	rr := httptest.NewRecorder()
	s.handleContainerInspect(rr, req, state.ID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var body map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	stateObj, _ := body["State"].(map[string]interface{})
	health, ok := stateObj["Health"].(map[string]interface{})
	if !ok {
		t.Fatalf("State.Health=%T %#v, want object", stateObj["Health"], stateObj["Health"])
	}
	if health["Status"] != "healthy" {
		t.Fatalf("State.Health.Status=%v", health["Status"])
	}
}

func TestContainerInspectIncludesPublishedPortBindings(t *testing.T) {
	rt := dokiruntime.NewRuntime(t.TempDir(), nil)
	state := &dokiruntime.ContainerState{
		ID:      "pg-inspect-ports",
		Status:  "running",
		Created: time.Now().UTC(),
		Config: &dokiruntime.Config{
			ImageRef: "postgres:16-alpine",
			Ports: []common.Port{{
				PrivatePort: 5432,
				PublicPort:  5750,
				Type:        common.ProtocolTCP,
			}},
		},
	}
	if err := rt.SaveState(state); err != nil {
		t.Fatal(err)
	}
	s := &Server{runtime: rt}
	req := httptest.NewRequest(http.MethodGet, "/containers/pg-inspect-ports/json", nil)
	rr := httptest.NewRecorder()
	s.handleContainerInspect(rr, req, state.ID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var body map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	ns, _ := body["NetworkSettings"].(map[string]interface{})
	ports, ok := ns["Ports"].(map[string]interface{})
	if !ok {
		t.Fatalf("NetworkSettings.Ports=%T %#v, want object", ns["Ports"], ns["Ports"])
	}
	bindings, ok := ports["5432/tcp"].([]interface{})
	if !ok || len(bindings) != 1 {
		t.Fatalf("5432/tcp bindings=%T %#v", ports["5432/tcp"], ports["5432/tcp"])
	}
	binding, _ := bindings[0].(map[string]interface{})
	if binding["HostPort"] != "5750" {
		t.Fatalf("HostPort=%v", binding["HostPort"])
	}
}
