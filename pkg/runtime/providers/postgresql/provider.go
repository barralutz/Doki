package postgresql

import (
	"context"
	"fmt"
	"strings"

	dr "github.com/OpceanAI/Doki/pkg/runtime"
)

const providerID = "postgresql"

type Provider struct {
	cacheRoot    string
	termuxPrefix string
}

type imageContract struct {
	Version string
	Major   string
	SHA256  string
}

func New(cacheRoot, termuxPrefix string) *Provider {
	return &Provider{cacheRoot: cacheRoot, termuxPrefix: termuxPrefix}
}

func (p *Provider) ID() string { return providerID }

func (p *Provider) Match(_ context.Context, desc dr.WorkloadDescriptor) dr.ProviderMatch {
	if !isOfficialPostgresRef(desc.ImageRef) {
		return dr.ProviderMatch{}
	}
	return dr.ProviderMatch{
		Matched:  true,
		Required: true,
		Reason:   "official PostgreSQL image requires Android-native runtime",
	}
}

func (p *Provider) Ensure(_ context.Context, desc dr.WorkloadDescriptor) error {
	_, err := imageContractFromDescriptor(desc)
	return err
}

func (p *Provider) Prepare(context.Context, dr.WorkloadDescriptor) (*dr.PreparedWorkload, error) {
	return nil, fmt.Errorf("PostgreSQL provider prepare is not implemented")
}

func (p *Provider) PrepareExec(context.Context, dr.WorkloadDescriptor, *dr.ExecConfig) (*dr.PreparedExec, error) {
	return nil, fmt.Errorf("PostgreSQL provider exec is not implemented")
}

func (p *Provider) Cleanup(context.Context, dr.WorkloadDescriptor) error { return nil }

func imageContractFromDescriptor(desc dr.WorkloadDescriptor) (imageContract, error) {
	if desc.ImageConfig == nil {
		return imageContract{}, fmt.Errorf("PostgreSQL image %s is missing OCI image configuration", desc.ImageRef)
	}
	env := envMap(desc.ImageConfig.Env)
	version := strings.TrimSpace(env["PG_VERSION"])
	if version == "" {
		return imageContract{}, fmt.Errorf("PostgreSQL image %s is missing authoritative PG_VERSION", desc.ImageRef)
	}
	major := strings.TrimSpace(env["PG_MAJOR"])
	if major == "" {
		return imageContract{}, fmt.Errorf("PostgreSQL image %s is missing authoritative PG_MAJOR", desc.ImageRef)
	}
	versionMajor := version
	if i := strings.IndexByte(versionMajor, '.'); i >= 0 {
		versionMajor = versionMajor[:i]
	}
	if major != versionMajor {
		return imageContract{}, fmt.Errorf("PostgreSQL image requests PG_VERSION %s but PG_MAJOR is %s", version, major)
	}
	return imageContract{
		Version: version,
		Major:   major,
		SHA256:  strings.TrimSpace(env["PG_SHA256"]),
	}, nil
}

func envMap(items []string) map[string]string {
	out := make(map[string]string, len(items))
	for _, item := range items {
		key, value, ok := strings.Cut(item, "=")
		if !ok || key == "" {
			continue
		}
		out[key] = value
	}
	return out
}

func isOfficialPostgresRef(ref string) bool {
	repo := strings.TrimSpace(ref)
	if repo == "" {
		return false
	}
	if at := strings.IndexByte(repo, '@'); at >= 0 {
		repo = repo[:at]
	}
	lastSlash := strings.LastIndexByte(repo, '/')
	if colon := strings.LastIndexByte(repo, ':'); colon > lastSlash {
		repo = repo[:colon]
	}
	switch repo {
	case "postgres",
		"library/postgres",
		"docker.io/postgres",
		"docker.io/library/postgres",
		"registry-1.docker.io/postgres",
		"registry-1.docker.io/library/postgres":
		return true
	default:
		return false
	}
}
