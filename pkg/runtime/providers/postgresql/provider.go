package postgresql

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net"
	"path/filepath"
	"strings"

	"github.com/OpceanAI/Doki/pkg/common"
	dr "github.com/OpceanAI/Doki/pkg/runtime"
)

const providerID = "postgresql"

type runtimeProvisioner interface {
	Ensure(context.Context, imageContract) (RuntimePaths, error)
}

type Provider struct {
	cacheRoot     string
	termuxPrefix  string
	provisioner   runtimeProvisioner
	clusterRunner clusterCommandRunner
}

type imageContract struct {
	Version string
	Major   string
	SHA256  string
}

func New(cacheRoot, termuxPrefix string) *Provider {
	return &Provider{cacheRoot: cacheRoot, termuxPrefix: termuxPrefix, provisioner: newProvisioner(cacheRoot, termuxPrefix), clusterRunner: execClusterRunner{}}
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

func (p *Provider) Ensure(ctx context.Context, desc dr.WorkloadDescriptor) error {
	contract, err := imageContractFromDescriptor(desc)
	if err != nil {
		return err
	}
	if p.provisioner == nil {
		return fmt.Errorf("PostgreSQL provider provisioner is not configured")
	}
	_, err = p.provisioner.Ensure(ctx, contract)
	return err
}

func (p *Provider) Prepare(ctx context.Context, desc dr.WorkloadDescriptor) (*dr.PreparedWorkload, error) {
	contract, err := imageContractFromDescriptor(desc)
	if err != nil {
		return nil, err
	}
	if p.provisioner == nil {
		return nil, fmt.Errorf("PostgreSQL provider provisioner is not configured")
	}
	paths, err := p.provisioner.Ensure(ctx, contract)
	if err != nil {
		return nil, err
	}
	cfg, err := clusterConfigFromDescriptor(desc)
	if err != nil {
		return nil, err
	}
	if err := p.ensureCluster(ctx, paths, cfg); err != nil {
		return nil, err
	}
	args, err := postgresServerArgs(desc.Args)
	if err != nil {
		return nil, err
	}
	host, privatePort, forwards, err := postgresNetworkPlan(desc)
	if err != nil {
		return nil, err
	}
	args = append(args, "-h", host, "-p", fmt.Sprintf("%d", privatePort))
	return &dr.PreparedWorkload{
		Executable:   paths.Postgres,
		Args:         args,
		Env:          providerProcessEnv(paths, cfg.PGData, host, privatePort, p.effectiveTermuxPrefix()),
		Cwd:          "/",
		PortForwards: forwards,
	}, nil
}

func (p *Provider) PrepareExec(ctx context.Context, desc dr.WorkloadDescriptor, cfg *dr.ExecConfig) (*dr.PreparedExec, error) {
	if cfg == nil || len(cfg.Args) == 0 {
		return nil, fmt.Errorf("PostgreSQL provider exec requires a command")
	}
	if cfg.WorkingDir != "" && cfg.WorkingDir != "/" {
		return nil, fmt.Errorf("PostgreSQL provider exec does not support container working directory %q", cfg.WorkingDir)
	}
	contract, err := imageContractFromDescriptor(desc)
	if err != nil {
		return nil, err
	}
	if p.provisioner == nil {
		return nil, fmt.Errorf("PostgreSQL provider provisioner is not configured")
	}
	paths, err := p.provisioner.Ensure(ctx, contract)
	if err != nil {
		return nil, err
	}
	cluster, err := clusterConfigFromDescriptor(desc)
	if err != nil {
		return nil, err
	}

	first := cfg.Args[0]
	base := filepath.Base(first)
	var executable string
	switch base {
	case "pg_isready":
		executable = paths.PgIsReady
	case "psql":
		executable = paths.Psql
	case "postgres":
		executable = paths.Postgres
	case "sh":
		if first != "sh" && first != "/bin/sh" && first != filepath.Join(p.effectiveTermuxPrefix(), "bin", "sh") {
			return nil, fmt.Errorf("PostgreSQL provider exec rejects shell path %q", first)
		}
		executable = filepath.Join(p.effectiveTermuxPrefix(), "bin", "sh")
	default:
		return nil, fmt.Errorf("PostgreSQL provider exec command %q is not supported", first)
	}

	host, privatePort, _, err := postgresNetworkPlan(desc)
	if err != nil {
		return nil, err
	}
	env := append([]string(nil), cfg.Env...)
	env = append(env, providerProcessEnv(paths, cluster.PGData, host, privatePort, p.effectiveTermuxPrefix())...)
	return &dr.PreparedExec{
		Executable: executable,
		Args:       append([]string(nil), cfg.Args[1:]...),
		Env:        env,
		Cwd:        "/",
	}, nil
}

func postgresServerArgs(args []string) ([]string, error) {
	argv := append([]string(nil), args...)
	if len(argv) > 0 && filepath.Base(argv[0]) == "docker-entrypoint.sh" {
		argv = argv[1:]
	}
	if len(argv) == 0 {
		return nil, nil
	}
	if filepath.Base(argv[0]) == "postgres" {
		return argv[1:], nil
	}
	if strings.HasPrefix(argv[0], "-") {
		return argv, nil
	}
	return nil, fmt.Errorf("PostgreSQL provider does not support image command %q; expected postgres", argv[0])
}

func (p *Provider) effectiveTermuxPrefix() string {
	if strings.TrimSpace(p.termuxPrefix) != "" {
		return p.termuxPrefix
	}
	return "/data/data/com.termux/files/usr"
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

func endpointForContainer(id string) net.IP {
	sum := sha256.Sum256([]byte(id))
	return net.IPv4(127, 64+(sum[0]%64), sum[1], 1+(sum[2]%254))
}

func postgresNetworkPlan(desc dr.WorkloadDescriptor) (string, uint16, []dr.PortForward, error) {
	const privatePort uint16 = 5432
	host := endpointForContainer(desc.ContainerID).String()
	forwards := make([]dr.PortForward, 0, len(desc.Ports))
	for _, port := range desc.Ports {
		if port.PublicPort == 0 {
			continue
		}
		proto := port.Type
		if proto == "" {
			proto = common.ProtocolTCP
		}
		if proto != common.ProtocolTCP {
			return "", 0, nil, fmt.Errorf("PostgreSQL Android provider does not support published %s ports", proto)
		}
		if port.PrivatePort != privatePort {
			return "", 0, nil, fmt.Errorf("PostgreSQL Android provider only supports published container port 5432, got %d", port.PrivatePort)
		}
		listenHost := strings.TrimSpace(port.IP)
		if listenHost == "" {
			listenHost = "0.0.0.0"
		}
		forwards = append(forwards, dr.PortForward{
			ListenHost: listenHost,
			ListenPort: port.PublicPort,
			TargetHost: host,
			TargetPort: privatePort,
		})
	}
	return host, privatePort, forwards, nil
}
