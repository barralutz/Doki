package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAndroidProviderStateRoundTrip(t *testing.T) {
	rt := NewRuntime(t.TempDir(), nil)
	id := "container-one"
	if err := rt.saveAndroidProviderState(id, AndroidProviderState{Version: 1, ProviderID: "fake", MatchReason: "test reason"}); err != nil {
		t.Fatal(err)
	}
	got, err := rt.loadAndroidProviderState(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != 1 || got.ProviderID != "fake" || got.MatchReason != "test reason" {
		t.Fatalf("state = %+v", got)
	}
	path := filepath.Join(rt.root, "containers", id, "android-provider.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("provider state file missing: %v", err)
	}
}

func TestAndroidProviderStateRejectsMissingCorruptAndUnsupported(t *testing.T) {
	rt := NewRuntime(t.TempDir(), nil)
	if _, err := rt.loadAndroidProviderState("missing"); err == nil {
		t.Fatal("missing state unexpectedly loaded")
	}
	dir := filepath.Join(rt.root, "containers", "bad")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "android-provider.json")
	if err := os.WriteFile(path, []byte("{"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.loadAndroidProviderState("bad"); err == nil || !strings.Contains(err.Error(), "parse Android provider state") {
		t.Fatalf("corrupt state error = %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"version":2,"providerId":"fake"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.loadAndroidProviderState("bad"); err == nil || !strings.Contains(err.Error(), "unsupported Android provider state version 2") {
		t.Fatalf("unsupported version error = %v", err)
	}
}

func TestAndroidProviderStateRejectsEmptyProviderIDAndDoesNotPersistSecrets(t *testing.T) {
	rt := NewRuntime(t.TempDir(), nil)
	if err := rt.saveAndroidProviderState("empty", AndroidProviderState{Version: 1}); err == nil {
		t.Fatal("empty provider ID unexpectedly accepted")
	}
	id := "safe"
	secret := "POSTGRES_PASSWORD=super-secret-value"
	if err := rt.saveAndroidProviderState(id, AndroidProviderState{Version: 1, ProviderID: "fake", MatchReason: "requires native Android runtime"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(rt.root, "containers", id, "android-provider.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) || strings.Contains(string(raw), "super-secret-value") {
		t.Fatalf("provider state leaked secret: %s", raw)
	}
}
