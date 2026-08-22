package postgresql

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OpceanAI/Doki/pkg/common"
	dr "github.com/OpceanAI/Doki/pkg/runtime"
)

func descriptorForCluster(hostPath string, userEnv ...string) dr.WorkloadDescriptor {
	return dr.WorkloadDescriptor{
		ContainerID: "pg-cluster",
		ImageRef:    "postgres:16-alpine",
		ImageConfig: &dr.ImageOCIConfig{Env: []string{
			"PG_MAJOR=16",
			"PG_VERSION=16.15",
			"PG_SHA256=" + verifiedPGSHA256,
			"PGDATA=/var/lib/postgresql/data",
		}},
		Env: userEnv,
		Mounts: []dr.WorkloadMount{{
			Mount:    common.Mount{Type: common.MountVolume, Source: "postgres_data", Target: "/var/lib/postgresql/data"},
			HostPath: hostPath,
		}},
	}
}

func TestClusterConfigUsesUserOverridesAndResolvedVolume(t *testing.T) {
	host := t.TempDir()
	desc := descriptorForCluster(host,
		"POSTGRES_USER=techservice",
		"POSTGRES_PASSWORD=secret",
		"POSTGRES_DB=techservice",
		"PGDATA=/var/lib/postgresql/data/pgdata",
	)
	cfg, err := clusterConfigFromDescriptor(desc)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.User != "techservice" || cfg.Password != "secret" || cfg.Database != "techservice" {
		t.Fatalf("config=%+v", cfg)
	}
	want := filepath.Join(host, "pgdata")
	if cfg.PGData != want {
		t.Fatalf("PGData=%q, want %q", cfg.PGData, want)
	}
}

func TestClusterConfigDefaultsUserDatabaseAndImagePGData(t *testing.T) {
	host := t.TempDir()
	cfg, err := clusterConfigFromDescriptor(descriptorForCluster(host, "POSTGRES_PASSWORD=secret"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.User != "postgres" || cfg.Database != "postgres" || cfg.PGData != host {
		t.Fatalf("config=%+v", cfg)
	}
}

func TestClusterConfigRejectsUnsupportedOfficialImageOptions(t *testing.T) {
	for _, item := range []string{
		"POSTGRES_INITDB_ARGS=--data-checksums",
		"POSTGRES_HOST_AUTH_METHOD=trust",
		"POSTGRES_INITDB_WALDIR=/wal",
	} {
		t.Run(strings.SplitN(item, "=", 2)[0], func(t *testing.T) {
			_, err := clusterConfigFromDescriptor(descriptorForCluster(t.TempDir(), "POSTGRES_PASSWORD=secret", item))
			if err == nil || !strings.Contains(err.Error(), strings.SplitN(item, "=", 2)[0]) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestClusterConfigRequiresResolvedPGDataMount(t *testing.T) {
	desc := descriptorForCluster("", "POSTGRES_PASSWORD=secret")
	_, err := clusterConfigFromDescriptor(desc)
	if err == nil || !strings.Contains(err.Error(), "PGDATA") {
		t.Fatalf("error=%v", err)
	}
}

type clusterCall struct {
	name       string
	args       []string
	stdin      string
	password   string
	pwfileMode os.FileMode
}

type fakeClusterRunner struct {
	calls []clusterCall
}

func (f *fakeClusterRunner) Run(_ context.Context, _ string, stdin string, _ []string, name string, args ...string) ([]byte, error) {
	call := clusterCall{name: name, args: append([]string(nil), args...), stdin: stdin}
	for _, arg := range args {
		if strings.HasPrefix(arg, "--pwfile=") {
			path := strings.TrimPrefix(arg, "--pwfile=")
			if info, err := os.Stat(path); err == nil {
				call.pwfileMode = info.Mode().Perm()
			}
			if b, err := os.ReadFile(path); err == nil {
				call.password = string(b)
			}
		}
	}
	f.calls = append(f.calls, call)
	if strings.HasSuffix(name, "/initdb") {
		for i := 0; i+1 < len(args); i++ {
			if args[i] == "-D" {
				if err := os.MkdirAll(args[i+1], 0700); err != nil {
					return nil, err
				}
				if err := os.WriteFile(filepath.Join(args[i+1], "PG_VERSION"), []byte("16\n"), 0600); err != nil {
					return nil, err
				}
				break
			}
		}
	}
	return nil, nil
}

func fakeRuntimePaths(root string) RuntimePaths {
	return RuntimePaths{
		Root: root, BinDir: filepath.Join(root, "bin"),
		Postgres:  filepath.Join(root, "bin", "postgres"),
		InitDB:    filepath.Join(root, "bin", "initdb"),
		PgIsReady: filepath.Join(root, "bin", "pg_isready"),
		Psql:      filepath.Join(root, "bin", "psql"),
	}
}

func TestEnsureClusterInitializesEmptyDataDirectoryOnce(t *testing.T) {
	pgdata := filepath.Join(t.TempDir(), "data")
	runner := &fakeClusterRunner{}
	p := &Provider{clusterRunner: runner}
	paths := fakeRuntimePaths("/provider/16.15")
	cfg := clusterConfig{PGData: pgdata, User: "techservice", Password: "secret", Database: "techservice", Major: "16"}

	if err := p.ensureCluster(context.Background(), paths, cfg); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("calls=%+v, want initdb + requested database creation", runner.calls)
	}
	call := runner.calls[0]
	if call.name != paths.InitDB || call.password != "secret\n" || call.pwfileMode != 0600 {
		t.Fatalf("initdb call=%+v", call)
	}
	joined := strings.Join(call.args, " ")
	for _, want := range []string{"-D " + pgdata, "--username=techservice", "--auth-local=trust", "--auth-host=scram-sha-256"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("initdb args=%q missing %q", joined, want)
		}
	}
	if strings.Contains(joined, "secret") {
		t.Fatalf("password leaked into argv: %q", joined)
	}
	dbCall := runner.calls[1]
	if dbCall.name != paths.Postgres || !strings.Contains(strings.Join(dbCall.args, " "), "--single") {
		t.Fatalf("database creation call=%+v", dbCall)
	}
	if dbCall.stdin != "CREATE DATABASE \"techservice\" OWNER \"techservice\";\n" {
		t.Fatalf("database creation SQL=%q", dbCall.stdin)
	}
}

func TestEnsureClusterCreatesDifferentDatabaseWithSingleUserBackend(t *testing.T) {
	pgdata := filepath.Join(t.TempDir(), "data")
	runner := &fakeClusterRunner{}
	p := &Provider{clusterRunner: runner}
	paths := fakeRuntimePaths("/provider/16.15")
	cfg := clusterConfig{PGData: pgdata, User: "techservice", Password: "secret", Database: "appdb", Major: "16"}

	if err := p.ensureCluster(context.Background(), paths, cfg); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("calls=%+v, want initdb + single-user database creation", runner.calls)
	}
	call := runner.calls[1]
	if call.name != paths.Postgres || !strings.Contains(strings.Join(call.args, " "), "--single") {
		t.Fatalf("single-user call=%+v", call)
	}
	if call.stdin != "CREATE DATABASE \"appdb\" OWNER \"techservice\";\n" {
		t.Fatalf("SQL=%q", call.stdin)
	}
}

func TestEnsureClusterReusesMatchingExistingCluster(t *testing.T) {
	pgdata := t.TempDir()
	if err := os.WriteFile(filepath.Join(pgdata, "PG_VERSION"), []byte("16\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runner := &fakeClusterRunner{}
	p := &Provider{clusterRunner: runner}
	if err := p.ensureCluster(context.Background(), fakeRuntimePaths("/provider/16.15"), clusterConfig{PGData: pgdata, Major: "16"}); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("existing cluster unexpectedly initialized: %+v", runner.calls)
	}
}

func TestEnsureClusterRejectsWrongMajorAndPartialDirectory(t *testing.T) {
	t.Run("wrong major", func(t *testing.T) {
		pgdata := t.TempDir()
		_ = os.WriteFile(filepath.Join(pgdata, "PG_VERSION"), []byte("17\n"), 0644)
		p := &Provider{clusterRunner: &fakeClusterRunner{}}
		err := p.ensureCluster(context.Background(), fakeRuntimePaths("/provider/16.15"), clusterConfig{PGData: pgdata, Major: "16"})
		if err == nil || !strings.Contains(err.Error(), "17") || !strings.Contains(err.Error(), "16") {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("partial", func(t *testing.T) {
		pgdata := t.TempDir()
		_ = os.WriteFile(filepath.Join(pgdata, "partial"), []byte("x"), 0644)
		p := &Provider{clusterRunner: &fakeClusterRunner{}}
		err := p.ensureCluster(context.Background(), fakeRuntimePaths("/provider/16.15"), clusterConfig{PGData: pgdata, Major: "16", Password: "secret"})
		if err == nil || !strings.Contains(err.Error(), "non-empty") {
			t.Fatalf("error=%v", err)
		}
	})
}

func TestEnsureClusterRequiresPasswordForNewCluster(t *testing.T) {
	p := &Provider{clusterRunner: &fakeClusterRunner{}}
	err := p.ensureCluster(context.Background(), fakeRuntimePaths("/provider/16.15"), clusterConfig{PGData: filepath.Join(t.TempDir(), "data"), Major: "16", User: "postgres", Database: "postgres"})
	if err == nil || !strings.Contains(err.Error(), "POSTGRES_PASSWORD") {
		t.Fatalf("error=%v", err)
	}
}
