package volume

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OpceanAI/Doki/pkg/common"
)

func TestManagerCreateUsesMetadataAndDataDirectories(t *testing.T) {
	root := t.TempDir()
	m, err := NewManager(root)
	if err != nil {
		t.Fatal(err)
	}

	v, err := m.Create("compose_data", "", nil, map[string]string{"com.docker.compose.project": "demo"})
	if err != nil {
		t.Fatal(err)
	}

	wantData := filepath.Join(root, "compose_data", "_data")
	if v.Mountpoint != wantData {
		t.Fatalf("Mountpoint = %q, want %q", v.Mountpoint, wantData)
	}
	if st, err := os.Stat(wantData); err != nil || !st.IsDir() {
		t.Fatalf("_data missing or not directory: stat=%v err=%v", st, err)
	}
	if _, err := os.Stat(filepath.Join(root, "compose_data", "volume.json")); err != nil {
		t.Fatalf("metadata missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wantData, "volume.json")); !os.IsNotExist(err) {
		t.Fatalf("metadata leaked into volume data: %v", err)
	}
}

func TestManagerResolveReturnsDataDir(t *testing.T) {
	root := t.TempDir()
	m, err := NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Create("db", "local", nil, nil); err != nil {
		t.Fatal(err)
	}

	got, err := m.Resolve("db")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "db", "_data")
	if got != want {
		t.Fatalf("Resolve(db) = %q, want %q", got, want)
	}
}

func writeLegacyVolumeMetadata(t *testing.T, root, name string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	info := common.VolumeInfo{Name: name, Driver: "local", Mountpoint: dir}
	raw, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "volume.json"), raw, 0644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestManagerMigratesLegacyVolumeIntoDataDir(t *testing.T) {
	root := t.TempDir()
	dir := writeLegacyVolumeMetadata(t, root, "legacy")
	if err := os.WriteFile(filepath.Join(dir, "PG_VERSION"), []byte("16\n"), 0644); err != nil {
		t.Fatal(err)
	}

	m, err := NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	v, err := m.Get("legacy")
	if err != nil {
		t.Fatal(err)
	}
	wantData := filepath.Join(dir, "_data")
	if v.Mountpoint != wantData {
		t.Fatalf("Mountpoint = %q, want %q", v.Mountpoint, wantData)
	}
	if got, err := os.ReadFile(filepath.Join(wantData, "PG_VERSION")); err != nil || string(got) != "16\n" {
		t.Fatalf("migrated payload = %q, err=%v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "PG_VERSION")); !os.IsNotExist(err) {
		t.Fatalf("legacy payload still at metadata root: %v", err)
	}
}

func TestManagerResumesInterruptedLegacyMigration(t *testing.T) {
	root := t.TempDir()
	dir := writeLegacyVolumeMetadata(t, root, "legacy")
	dataDir := filepath.Join(dir, "_data")
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "already-moved"), []byte("one"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "still-legacy"), []byte("two"), 0644); err != nil {
		t.Fatal(err)
	}

	m, err := NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	v, err := m.Get("legacy")
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"already-moved": "one", "still-legacy": "two"} {
		got, err := os.ReadFile(filepath.Join(v.Mountpoint, name))
		if err != nil || string(got) != want {
			t.Fatalf("%s = %q, err=%v, want %q", name, got, err, want)
		}
	}
}

func TestManagerRejectsAmbiguousLegacyMigration(t *testing.T) {
	root := t.TempDir()
	dir := writeLegacyVolumeMetadata(t, root, "legacy")
	dataDir := filepath.Join(dir, "_data")
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "collision"), []byte("legacy"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "collision"), []byte("migrated"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := NewManager(root)
	if err == nil || !strings.Contains(err.Error(), "ambiguous legacy volume migration") {
		t.Fatalf("NewManager() error = %v, want ambiguous migration error", err)
	}
	for path, want := range map[string]string{
		filepath.Join(dir, "collision"):     "legacy",
		filepath.Join(dataDir, "collision"): "migrated",
	} {
		got, readErr := os.ReadFile(path)
		if readErr != nil || string(got) != want {
			t.Fatalf("%s = %q, err=%v, want %q", path, got, readErr, want)
		}
	}
}
