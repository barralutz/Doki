package postgresql

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeCommandRunner struct {
	versionByExecutable map[string]string
	calls               []string
}

func (f *fakeCommandRunner) Run(_ context.Context, dir, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, strings.Join(append([]string{dir, name}, args...), " "))
	if len(args) == 1 && args[0] == "--version" {
		if v, ok := f.versionByExecutable[name]; ok {
			return []byte(v + "\n"), nil
		}
	}
	return nil, fmt.Errorf("unexpected command: %s %v", name, args)
}

type fakeSourceBuilder struct {
	calls   int
	onBuild func(string)
}

func (f *fakeSourceBuilder) Build(_ context.Context, _ string, installDir, _ string) error {
	f.calls++
	if err := writeFakePostgresRuntime(installDir); err != nil {
		return err
	}
	if f.onBuild != nil {
		f.onBuild(installDir)
	}
	return nil
}

func writeFakePostgresRuntime(root string) error {
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		return err
	}
	for _, name := range []string{"postgres", "initdb", "pg_isready", "psql"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("fake"), 0755); err != nil {
			return err
		}
	}
	return nil
}

func contractForProvisioning(version, sha string) imageContract {
	major := version
	if i := strings.IndexByte(major, '.'); i >= 0 {
		major = major[:i]
	}
	return imageContract{Version: version, Major: major, SHA256: sha}
}

func TestProvisionerReusesExactCachedRuntime(t *testing.T) {
	cache := t.TempDir()
	prefix := t.TempDir()
	root := filepath.Join(cache, "16.15", "arm64")
	if err := writeFakePostgresRuntime(root); err != nil {
		t.Fatal(err)
	}
	postgres := filepath.Join(root, "bin", "postgres")
	runner := &fakeCommandRunner{versionByExecutable: map[string]string{postgres: "postgres (PostgreSQL) 16.15"}}
	p := newProvisioner(cache, prefix)
	p.arch = "arm64"
	p.runner = runner
	p.download = func(context.Context, string, string) error { t.Fatal("download called for cache hit"); return nil }
	p.builder = &fakeSourceBuilder{}

	got, err := p.Ensure(context.Background(), contractForProvisioning("16.15", verifiedPGSHA256))
	if err != nil {
		t.Fatal(err)
	}
	if got.Root != root || got.Postgres != postgres {
		t.Fatalf("runtime=%+v, want root %s", got, root)
	}
}

func TestProvisionerReusesExactTermuxRuntime(t *testing.T) {
	cache := t.TempDir()
	prefix := t.TempDir()
	if err := writeFakePostgresRuntime(prefix); err != nil {
		t.Fatal(err)
	}
	postgres := filepath.Join(prefix, "bin", "postgres")
	runner := &fakeCommandRunner{versionByExecutable: map[string]string{postgres: "postgres (PostgreSQL) 16.15"}}
	p := newProvisioner(cache, prefix)
	p.arch = "arm64"
	p.runner = runner
	p.download = func(context.Context, string, string) error {
		t.Fatal("download called for exact Termux runtime")
		return nil
	}
	p.builder = &fakeSourceBuilder{}

	got, err := p.Ensure(context.Background(), contractForProvisioning("16.15", verifiedPGSHA256))
	if err != nil {
		t.Fatal(err)
	}
	if got.Root != prefix || got.Postgres != postgres {
		t.Fatalf("runtime=%+v, want Termux prefix %s", got, prefix)
	}
}

func TestProvisionerDoesNotSubstituteWrongTermuxVersion(t *testing.T) {
	cache := t.TempDir()
	prefix := t.TempDir()
	if err := writeFakePostgresRuntime(prefix); err != nil {
		t.Fatal(err)
	}
	termuxPostgres := filepath.Join(prefix, "bin", "postgres")
	archive := []byte("verified postgresql source fixture")
	sum := fmt.Sprintf("%x", sha256.Sum256(archive))
	runner := &fakeCommandRunner{versionByExecutable: map[string]string{
		termuxPostgres: "postgres (PostgreSQL) 18.2",
	}}
	builder := &fakeSourceBuilder{}
	var gotURL string
	p := newProvisioner(cache, prefix)
	p.arch = "arm64"
	p.runner = runner
	p.download = func(_ context.Context, url, dst string) error {
		gotURL = url
		return os.WriteFile(dst, archive, 0644)
	}
	builder.onBuild = func(installDir string) {
		runner.versionByExecutable[filepath.Join(installDir, "bin", "postgres")] = "postgres (PostgreSQL) 16.15"
	}
	p.builder = builder

	got, err := p.Ensure(context.Background(), contractForProvisioning("16.15", sum))
	if err != nil {
		t.Fatal(err)
	}
	if builder.calls != 1 {
		t.Fatalf("builder calls=%d, want 1", builder.calls)
	}
	if got.Root == prefix {
		t.Fatalf("wrong-version Termux runtime was reused: %+v", got)
	}
	if gotURL != "https://ftp.postgresql.org/pub/source/v16.15/postgresql-16.15.tar.bz2" {
		t.Fatalf("source URL=%q", gotURL)
	}
}

func TestProvisionerRejectsChecksumMismatchBeforeBuild(t *testing.T) {
	cache := t.TempDir()
	prefix := t.TempDir()
	archive := []byte("wrong bytes")
	builder := &fakeSourceBuilder{}
	p := newProvisioner(cache, prefix)
	p.arch = "arm64"
	p.runner = &fakeCommandRunner{versionByExecutable: map[string]string{}}
	p.download = func(_ context.Context, _, dst string) error { return os.WriteFile(dst, archive, 0644) }
	p.builder = builder

	_, err := p.Ensure(context.Background(), contractForProvisioning("16.15", verifiedPGSHA256))
	if err == nil || !strings.Contains(err.Error(), "SHA256") {
		t.Fatalf("error=%v, want checksum mismatch", err)
	}
	if builder.calls != 0 {
		t.Fatalf("builder called after checksum failure: %d", builder.calls)
	}
}

func TestProvisionerRejectsMissingChecksumBeforeDownload(t *testing.T) {
	p := newProvisioner(t.TempDir(), t.TempDir())
	p.arch = "arm64"
	p.runner = &fakeCommandRunner{versionByExecutable: map[string]string{}}
	called := false
	p.download = func(context.Context, string, string) error { called = true; return nil }
	p.builder = &fakeSourceBuilder{}

	_, err := p.Ensure(context.Background(), contractForProvisioning("16.15", ""))
	if err == nil || !strings.Contains(err.Error(), "PG_SHA256") {
		t.Fatalf("error=%v, want missing checksum", err)
	}
	if called {
		t.Fatal("download occurred without authoritative checksum")
	}
}

type recordedCommand struct {
	dir  string
	name string
	args []string
}

type recordingRunner struct {
	calls []recordedCommand
}

func (r *recordingRunner) Run(_ context.Context, dir, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, recordedCommand{dir: dir, name: name, args: append([]string(nil), args...)})
	return nil, nil
}

func writeFakeTool(t *testing.T, prefix, name string) {
	t.Helper()
	path := filepath.Join(prefix, "bin", name)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("tool"), 0755); err != nil {
		t.Fatal(err)
	}
}

func TestNativeSourceBuilderUsesVendoredAndroidPatchesAndTermuxToolchain(t *testing.T) {
	prefix := t.TempDir()
	for _, tool := range []string{"tar", "patch", "env", "make", "clang"} {
		writeFakeTool(t, prefix, tool)
	}
	stage := t.TempDir()
	archive := filepath.Join(stage, "postgresql-16.15.tar.bz2")
	if err := os.WriteFile(archive, []byte("fixture"), 0644); err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{}
	builder := &nativeSourceBuilder{runner: runner}
	install := filepath.Join(stage, "install")

	if err := builder.Build(context.Background(), archive, install, prefix); err != nil {
		t.Fatal(err)
	}

	patchCalls := 0
	configureSeen := false
	makeSeen := false
	installSeen := false
	for _, call := range runner.calls {
		if call.name == filepath.Join(prefix, "bin", "patch") {
			patchCalls++
		}
		joined := strings.Join(call.args, " ")
		if call.name == filepath.Join(prefix, "bin", "env") && strings.Contains(joined, "/configure") {
			configureSeen = true
			for _, required := range []string{
				"USE_UNNAMED_POSIX_SEMAPHORES=1",
				"ZIC=" + filepath.Join(stage, "src", "src", "timezone", "zic"),
				"--with-icu",
				"--with-libxml",
				"--with-openssl",
			} {
				if !strings.Contains(joined, required) {
					t.Fatalf("configure command missing %q: %s", required, joined)
				}
			}
		}
		if call.name == filepath.Join(prefix, "bin", "env") && strings.Contains(joined, filepath.Join(prefix, "bin", "make")+" -j2") {
			makeSeen = true
		}
		if call.name == filepath.Join(prefix, "bin", "env") && strings.Contains(joined, filepath.Join(prefix, "bin", "make")+" install") {
			installSeen = true
		}
	}
	if patchCalls != 6 {
		t.Fatalf("patch calls=%d, want 6 vendored Android compatibility patches", patchCalls)
	}
	if !configureSeen || !makeSeen || !installSeen {
		t.Fatalf("configure=%v make=%v install=%v calls=%+v", configureSeen, makeSeen, installSeen, runner.calls)
	}

	entries, err := os.ReadDir(filepath.Join(stage, "patches"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 6 {
		t.Fatalf("materialized patches=%d, want 6", len(entries))
	}
	for _, entry := range entries {
		raw, err := os.ReadFile(filepath.Join(stage, "patches", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "@TERMUX_PREFIX@") {
			t.Fatalf("patch %s still contains unresolved Termux prefix placeholder", entry.Name())
		}
	}
}
