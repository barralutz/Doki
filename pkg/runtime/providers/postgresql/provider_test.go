package postgresql

import (
	"context"
	"strings"
	"testing"

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
	err      error
}

func (f *fakeRuntimeProvisioner) Ensure(_ context.Context, contract imageContract) (RuntimePaths, error) {
	f.calls++
	f.contract = contract
	return RuntimePaths{}, f.err
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
