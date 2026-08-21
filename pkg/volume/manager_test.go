package volume

import (
	"os"
	"path/filepath"
	"testing"
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
