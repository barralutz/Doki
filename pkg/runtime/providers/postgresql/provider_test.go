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

const verifiedPGSHA256 = "c1575341fa7bd40f5274ea465b34390f4dc64cdd0770af327005caaeb9f6b7ed"

func pgDescriptor(ref string, imageEnv ...string) dr.WorkloadDescriptor {
	return dr.WorkloadDescriptor{
		ContainerID: "pg-test",
		ImageRef:    ref,
		ImageConfig: &dr.ImageOCIConfig{Env: imageEnv},
	}
}

func TestProviderMatchOfficialPostgresImages(t *testing.T) {
	p := New(t.TempDir(), "/data/data/com.termux/files/usr")
	for _, ref := range []string{
		"postgres:16-alpine",
		"library/postgres:16-alpine",
		"docker.io/library/postgres:16-alpine",
		"registry-1.docker.io/library/postgres:16-alpine",
		"docker.io/library/postgres@sha256:deadbeef",
	} {
		t.Run(ref, func(t *testing.T) {
			m := p.Match(context.Background(), pgDescriptor(ref,
				"PG_MAJOR=16",
				"PG_VERSION=16.15",
				"PG_SHA256="+verifiedPGSHA256,
			))
			if !m.Matched || !m.Required {
				t.Fatalf("match=%+v, want required official PostgreSQL match", m)
			}
			if !strings.Contains(m.Reason, "PostgreSQL") {
				t.Fatalf("reason=%q", m.Reason)
			}
		})
	}
}

func TestProviderDoesNotMatchLookalikeRepositories(t *testing.T) {
	p := New(t.TempDir(), "/data/data/com.termux/files/usr")
	for _, ref := range []string{
		"mycorp/postgres:16",
		"ghcr.io/acme/postgres:16",
		"postgresql:16",
		"postgis/postgis:16",
	} {
		t.Run(ref, func(t *testing.T) {
			m := p.Match(context.Background(), pgDescriptor(ref,
				"PG_MAJOR=16",
				"PG_VERSION=16.15",
				"PG_SHA256="+verifiedPGSHA256,
			))
			if m.Matched || m.Required {
				t.Fatalf("match=%+v, want no match", m)
			}
		})
	}
}

func TestProviderEnsureRejectsMissingAuthoritativeVersionMetadata(t *testing.T) {
	p := New(t.TempDir(), "/data/data/com.termux/files/usr")
	desc := pgDescriptor("postgres:16-alpine", "PG_MAJOR=16")
	m := p.Match(context.Background(), desc)
	if !m.Matched || !m.Required {
		t.Fatalf("match=%+v, official image must remain claimed so it cannot silently fall back", m)
	}
	if err := p.Ensure(context.Background(), desc); err == nil || !strings.Contains(err.Error(), "PG_VERSION") {
		t.Fatalf("Ensure error=%v, want missing PG_VERSION compatibility error", err)
	}
}

func TestImageContractRejectsInconsistentMajor(t *testing.T) {
	_, err := imageContractFromDescriptor(pgDescriptor("postgres:16-alpine",
		"PG_MAJOR=17",
		"PG_VERSION=16.15",
		"PG_SHA256="+verifiedPGSHA256,
	))
	if err == nil || !strings.Contains(err.Error(), "PG_MAJOR") || !strings.Contains(err.Error(), "16.15") {
		t.Fatalf("error=%v, want PG_MAJOR/version mismatch", err)
	}
}

func TestImageContractUsesImageMetadataNotUserEnvironment(t *testing.T) {
	desc := pgDescriptor("postgres:16-alpine",
		"PG_MAJOR=16",
		"PG_VERSION=16.15",
		"PG_SHA256="+verifiedPGSHA256,
	)
	desc.Env = []string{"PG_MAJOR=18", "PG_VERSION=18.2", "PG_SHA256=wrong"}
	contract, err := imageContractFromDescriptor(desc)
	if err != nil {
		t.Fatal(err)
	}
	if contract.Major != "16" || contract.Version != "16.15" || contract.SHA256 != verifiedPGSHA256 {
		t.Fatalf("contract=%+v", contract)
	}
}

type fakeRuntimeProvisioner struct {
	calls    int
	contract imageContract
	paths    RuntimePaths
	err      error
}

func (f *fakeRuntimeProvisioner) Ensure(_ context.Context, contract imageContract) (RuntimePaths, error) {
	f.calls++
	f.contract = contract
	return f.paths, f.err
}

func TestProviderEnsureDelegatesExactImageContractToProvisioner(t *testing.T) {
	fp := &fakeRuntimeProvisioner{}
	p := &Provider{provisioner: fp}
	desc := pgDescriptor("postgres:16-alpine",
		"PG_MAJOR=16",
		"PG_VERSION=16.15",
		"PG_SHA256="+verifiedPGSHA256,
	)
	if err := p.Ensure(context.Background(), desc); err != nil {
		t.Fatal(err)
	}
	if fp.calls != 1 {
		t.Fatalf("provisioner calls=%d, want 1", fp.calls)
	}
	if fp.contract.Version != "16.15" || fp.contract.Major != "16" || fp.contract.SHA256 != verifiedPGSHA256 {
		t.Fatalf("contract=%+v", fp.contract)
	}
}

func existingPostgresDescriptor(t *testing.T) dr.WorkloadDescriptor {
	t.Helper()
	host := t.TempDir()
	if err := os.WriteFile(filepath.Join(host, "PG_VERSION"), []byte("16\n"), 0600); err != nil {
		t.Fatal(err)
	}
	desc := pgDescriptor("postgres:16-alpine",
		"PG_MAJOR=16",
		"PG_VERSION=16.15",
		"PG_SHA256="+verifiedPGSHA256,
		"PGDATA=/var/lib/postgresql/data",
	)
	desc.ContainerID = "pg-prepare"
	desc.Args = []string{"docker-entrypoint.sh", "postgres"}
	desc.Env = []string{"POSTGRES_USER=techservice", "POSTGRES_PASSWORD=secret", "POSTGRES_DB=techservice"}
	desc.Mounts = []dr.WorkloadMount{{
		Mount:    common.Mount{Type: common.MountVolume, Source: "postgres_data", Target: "/var/lib/postgresql/data"},
		HostPath: host,
	}}
	return desc
}

func TestProviderPrepareUsesExactRuntimeAndStripsOfficialEntrypoint(t *testing.T) {
	desc := existingPostgresDescriptor(t)
	paths := fakeRuntimePaths("/provider/16.15")
	fp := &fakeRuntimeProvisioner{paths: paths}
	p := &Provider{provisioner: fp, clusterRunner: &fakeClusterRunner{}, termuxPrefix: "/data/data/com.termux/files/usr"}

	prepared, err := p.Prepare(context.Background(), desc)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Executable != paths.Postgres {
		t.Fatalf("Executable=%q, want %q", prepared.Executable, paths.Postgres)
	}
	endpoint := endpointForContainer(desc.ContainerID).String()
	if strings.Join(prepared.Args, " ") != "-h "+endpoint+" -p 5432" {
		t.Fatalf("Args=%q, want only provider endpoint args", prepared.Args)
	}
	joined := strings.Join(prepared.Env, "\n")
	for _, want := range []string{"PGDATA=" + desc.Mounts[0].HostPath, "PATH=" + paths.BinDir} {
		if !strings.Contains(joined, want) {
			t.Fatalf("env=%q missing %q", joined, want)
		}
	}
	if strings.Contains(joined, "POSTGRES_PASSWORD=secret") {
		t.Fatalf("server environment unnecessarily exposes POSTGRES_PASSWORD: %q", joined)
	}
}

func TestProviderPreparePreservesPostgresCommandOverrides(t *testing.T) {
	desc := existingPostgresDescriptor(t)
	desc.Args = []string{"docker-entrypoint.sh", "postgres", "-c", "max_connections=25"}
	paths := fakeRuntimePaths("/provider/16.15")
	p := &Provider{provisioner: &fakeRuntimeProvisioner{paths: paths}, clusterRunner: &fakeClusterRunner{}, termuxPrefix: "/data/data/com.termux/files/usr"}
	prepared, err := p.Prepare(context.Background(), desc)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := endpointForContainer(desc.ContainerID).String()
	if strings.Join(prepared.Args, " ") != "-c max_connections=25 -h "+endpoint+" -p 5432" {
		t.Fatalf("Args=%q", prepared.Args)
	}
}

func TestProviderPrepareRejectsNonPostgresImageCommand(t *testing.T) {
	desc := existingPostgresDescriptor(t)
	desc.Args = []string{"docker-entrypoint.sh", "bash"}
	p := &Provider{provisioner: &fakeRuntimeProvisioner{paths: fakeRuntimePaths("/provider/16.15")}, clusterRunner: &fakeClusterRunner{}}
	_, err := p.Prepare(context.Background(), desc)
	if err == nil || !strings.Contains(err.Error(), "command") {
		t.Fatalf("error=%v", err)
	}
}

func TestProviderPrepareExecUsesExactRuntimeUtilities(t *testing.T) {
	desc := existingPostgresDescriptor(t)
	paths := fakeRuntimePaths("/provider/16.15")
	p := &Provider{provisioner: &fakeRuntimeProvisioner{paths: paths}, clusterRunner: &fakeClusterRunner{}, termuxPrefix: "/termux"}

	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"pg_isready", "-U", "techservice", "-d", "techservice"}, paths.PgIsReady},
		{[]string{"psql", "-d", "techservice"}, paths.Psql},
		{[]string{"postgres", "--version"}, paths.Postgres},
	} {
		prepared, err := p.PrepareExec(context.Background(), desc, &dr.ExecConfig{Args: tc.args})
		if err != nil {
			t.Fatalf("args=%q: %v", tc.args, err)
		}
		if prepared.Executable != tc.want {
			t.Fatalf("args=%q executable=%q want=%q", tc.args, prepared.Executable, tc.want)
		}
		if strings.Join(prepared.Args, " ") != strings.Join(tc.args[1:], " ") {
			t.Fatalf("args=%q prepared args=%q", tc.args, prepared.Args)
		}
	}
}

func TestProviderPrepareExecSuppliesContainerPasswordForPrivateTCP(t *testing.T) {
	desc := existingPostgresDescriptor(t)
	paths := fakeRuntimePaths("/provider/16.15")
	p := &Provider{provisioner: &fakeRuntimeProvisioner{paths: paths}, clusterRunner: &fakeClusterRunner{}, termuxPrefix: "/termux"}

	prepared, err := p.PrepareExec(context.Background(), desc, &dr.ExecConfig{Args: []string{"psql", "-U", "techservice", "-d", "techservice"}})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(prepared.Env, "\n")
	if !strings.Contains(joined, "PGPASSWORD=secret") {
		t.Fatalf("env=%q missing PGPASSWORD derived from container POSTGRES_PASSWORD", joined)
	}
}

func TestProviderPrepareExecMapsHealthcheckShellToTermux(t *testing.T) {
	desc := existingPostgresDescriptor(t)
	paths := fakeRuntimePaths("/provider/16.15")
	p := &Provider{provisioner: &fakeRuntimeProvisioner{paths: paths}, clusterRunner: &fakeClusterRunner{}, termuxPrefix: "/termux"}
	prepared, err := p.PrepareExec(context.Background(), desc, &dr.ExecConfig{Args: []string{"/bin/sh", "-c", "pg_isready -U techservice -d techservice"}})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Executable != "/termux/bin/sh" || strings.Join(prepared.Args, " ") != "-c pg_isready -U techservice -d techservice" {
		t.Fatalf("prepared=%+v", prepared)
	}
	if !strings.Contains(strings.Join(prepared.Env, "\n"), "PATH="+paths.BinDir) {
		t.Fatalf("env=%q", prepared.Env)
	}
}

func TestProviderPrepareExecRejectsUnknownExecutable(t *testing.T) {
	desc := existingPostgresDescriptor(t)
	p := &Provider{provisioner: &fakeRuntimeProvisioner{paths: fakeRuntimePaths("/provider/16.15")}, clusterRunner: &fakeClusterRunner{}, termuxPrefix: "/termux"}
	for _, args := range [][]string{{"rm", "-rf", "/"}, {"/system/bin/id"}} {
		if _, err := p.PrepareExec(context.Background(), desc, &dr.ExecConfig{Args: args}); err == nil {
			t.Fatalf("args=%q unexpectedly accepted", args)
		}
	}
}

func TestEndpointForContainerIsDeterministicPrivateLoopback(t *testing.T) {
	a1 := endpointForContainer("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	a2 := endpointForContainer("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	b := endpointForContainer("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	if !a1.Equal(a2) {
		t.Fatalf("same container ID produced %s and %s", a1, a2)
	}
	if a1.Equal(b) {
		t.Fatalf("fixture IDs collided at %s", a1)
	}
	v4 := a1.To4()
	if v4 == nil || v4[0] != 127 || v4[1] < 64 || v4[1] > 127 || v4[3] == 0 || v4[3] == 255 {
		t.Fatalf("endpoint %s is outside 127.64.0.0/10 usable hosts", a1)
	}
}

func TestProviderPreparePlansPrivate5432AndPublishedPort(t *testing.T) {
	desc := existingPostgresDescriptor(t)
	desc.ContainerID = "postgres-port-plan"
	desc.Ports = []common.Port{{IP: "127.0.0.1", PrivatePort: 5432, PublicPort: 5750, Type: common.ProtocolTCP}}
	paths := fakeRuntimePaths("/provider/16.15")
	p := &Provider{provisioner: &fakeRuntimeProvisioner{paths: paths}, clusterRunner: &fakeClusterRunner{}, termuxPrefix: "/termux"}

	prepared, err := p.Prepare(context.Background(), desc)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := endpointForContainer(desc.ContainerID).String()
	joinedArgs := strings.Join(prepared.Args, " ")
	if !strings.Contains(joinedArgs, "-h "+endpoint) || !strings.Contains(joinedArgs, "-p 5432") {
		t.Fatalf("server args=%q", joinedArgs)
	}
	if len(prepared.PortForwards) != 1 {
		t.Fatalf("forwards=%+v", prepared.PortForwards)
	}
	forward := prepared.PortForwards[0]
	if forward.ListenHost != "127.0.0.1" || forward.ListenPort != 5750 || forward.TargetHost != endpoint || forward.TargetPort != 5432 {
		t.Fatalf("forward=%+v", forward)
	}
	joinedEnv := strings.Join(prepared.Env, "\n")
	if !strings.Contains(joinedEnv, "PGHOST="+endpoint) || !strings.Contains(joinedEnv, "PGPORT=5432") {
		t.Fatalf("env=%q", joinedEnv)
	}
}

func TestProviderPrepareDefaultsPublishedListenHostToAllInterfaces(t *testing.T) {
	desc := existingPostgresDescriptor(t)
	desc.Ports = []common.Port{{PrivatePort: 5432, PublicPort: 5750, Type: common.ProtocolTCP}}
	p := &Provider{provisioner: &fakeRuntimeProvisioner{paths: fakeRuntimePaths("/provider/16.15")}, clusterRunner: &fakeClusterRunner{}}
	prepared, err := p.Prepare(context.Background(), desc)
	if err != nil {
		t.Fatal(err)
	}
	if got := prepared.PortForwards[0].ListenHost; got != "0.0.0.0" {
		t.Fatalf("ListenHost=%q, want 0.0.0.0", got)
	}
}

func TestProviderPrepareRejectsUnsupportedPublishedPortProtocols(t *testing.T) {
	for _, port := range []common.Port{
		{PrivatePort: 5432, PublicPort: 5750, Type: common.ProtocolUDP},
		{PrivatePort: 9999, PublicPort: 5750, Type: common.ProtocolTCP},
	} {
		desc := existingPostgresDescriptor(t)
		desc.Ports = []common.Port{port}
		p := &Provider{provisioner: &fakeRuntimeProvisioner{paths: fakeRuntimePaths("/provider/16.15")}, clusterRunner: &fakeClusterRunner{}}
		if _, err := p.Prepare(context.Background(), desc); err == nil {
			t.Fatalf("port=%+v unexpectedly accepted", port)
		}
	}
}

func TestProviderPrepareExecTargetsSamePrivateEndpoint(t *testing.T) {
	desc := existingPostgresDescriptor(t)
	desc.ContainerID = "postgres-exec-endpoint"
	paths := fakeRuntimePaths("/provider/16.15")
	p := &Provider{provisioner: &fakeRuntimeProvisioner{paths: paths}, clusterRunner: &fakeClusterRunner{}, termuxPrefix: "/termux"}
	prepared, err := p.PrepareExec(context.Background(), desc, &dr.ExecConfig{Args: []string{"pg_isready", "-U", "techservice"}})
	if err != nil {
		t.Fatal(err)
	}
	endpoint := endpointForContainer(desc.ContainerID).String()
	joined := strings.Join(prepared.Env, "\n")
	if !strings.Contains(joined, "PGHOST="+endpoint) || !strings.Contains(joined, "PGPORT=5432") {
		t.Fatalf("env=%q", joined)
	}
}
