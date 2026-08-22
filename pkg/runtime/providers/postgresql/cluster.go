package postgresql

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	dr "github.com/OpceanAI/Doki/pkg/runtime"
)

type clusterConfig struct {
	PGData   string
	User     string
	Password string
	Database string
	Major    string
}

type clusterCommandRunner interface {
	Run(context.Context, string, string, []string, string, ...string) ([]byte, error)
}

type execClusterRunner struct{}

func (execClusterRunner) Run(ctx context.Context, dir, stdin string, env []string, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	if cmd.Dir == "" {
		cmd.Dir = "/"
	}
	cmd.Env = append(os.Environ(), env...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func clusterConfigFromDescriptor(desc dr.WorkloadDescriptor) (clusterConfig, error) {
	if desc.ImageConfig == nil {
		return clusterConfig{}, fmt.Errorf("PostgreSQL image configuration is missing")
	}
	contract, err := imageContractFromDescriptor(desc)
	if err != nil {
		return clusterConfig{}, err
	}
	env := envMap(desc.ImageConfig.Env)
	for k, v := range envMap(desc.Env) {
		env[k] = v
	}
	for _, key := range []string{
		"POSTGRES_INITDB_ARGS",
		"POSTGRES_HOST_AUTH_METHOD",
		"POSTGRES_INITDB_WALDIR",
		"POSTGRES_USER_FILE",
		"POSTGRES_PASSWORD_FILE",
		"POSTGRES_DB_FILE",
	} {
		if strings.TrimSpace(env[key]) != "" {
			return clusterConfig{}, fmt.Errorf("PostgreSQL Android provider does not support %s", key)
		}
	}

	user := env["POSTGRES_USER"]
	if user == "" {
		user = "postgres"
	}
	database := env["POSTGRES_DB"]
	if database == "" {
		database = user
	}
	pgdata := env["PGDATA"]
	if pgdata == "" {
		pgdata = "/var/lib/postgresql/data"
	}
	if !path.IsAbs(pgdata) {
		return clusterConfig{}, fmt.Errorf("PostgreSQL PGDATA must be an absolute container path: %q", pgdata)
	}
	pgdata = path.Clean(pgdata)

	hostPGData := ""
	for _, mount := range desc.Mounts {
		target := path.Clean(mount.Mount.Target)
		if pgdata != target && !strings.HasPrefix(pgdata, target+"/") {
			continue
		}
		if mount.HostPath == "" {
			return clusterConfig{}, fmt.Errorf("PostgreSQL PGDATA %s mount has no resolved host path", pgdata)
		}
		if mount.Mount.ReadOnly {
			return clusterConfig{}, fmt.Errorf("PostgreSQL PGDATA %s is mounted read-only", pgdata)
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(pgdata, target), "/")
		hostPGData = mount.HostPath
		if rel != "" {
			hostPGData = filepath.Join(mount.HostPath, filepath.FromSlash(rel))
		}
		break
	}
	if hostPGData == "" {
		return clusterConfig{}, fmt.Errorf("PostgreSQL PGDATA %s must be backed by a resolved volume or bind mount", pgdata)
	}
	return clusterConfig{
		PGData:   hostPGData,
		User:     user,
		Password: env["POSTGRES_PASSWORD"],
		Database: database,
		Major:    contract.Major,
	}, nil
}

func (p *Provider) ensureCluster(ctx context.Context, paths RuntimePaths, cfg clusterConfig) error {
	if p.clusterRunner == nil {
		return fmt.Errorf("PostgreSQL cluster runner is not configured")
	}
	if cfg.PGData == "" {
		return fmt.Errorf("PostgreSQL PGDATA host path is empty")
	}
	if err := os.MkdirAll(cfg.PGData, 0700); err != nil {
		return fmt.Errorf("create PostgreSQL PGDATA: %w", err)
	}
	if err := os.Chmod(cfg.PGData, 0700); err != nil {
		return fmt.Errorf("chmod PostgreSQL PGDATA: %w", err)
	}

	versionFile := filepath.Join(cfg.PGData, "PG_VERSION")
	if raw, err := os.ReadFile(versionFile); err == nil {
		got := strings.TrimSpace(string(raw))
		if got != cfg.Major {
			return fmt.Errorf("PostgreSQL PGDATA contains major version %s but image requires %s", got, cfg.Major)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read PostgreSQL PG_VERSION: %w", err)
	}

	entries, err := os.ReadDir(cfg.PGData)
	if err != nil {
		return fmt.Errorf("inspect PostgreSQL PGDATA: %w", err)
	}
	if len(entries) != 0 {
		return fmt.Errorf("PostgreSQL PGDATA is non-empty but has no PG_VERSION; refusing destructive initialization")
	}
	if cfg.Password == "" {
		return fmt.Errorf("POSTGRES_PASSWORD is required to initialize a new PostgreSQL cluster")
	}
	if cfg.User == "" || cfg.Database == "" {
		return fmt.Errorf("PostgreSQL user/database must not be empty")
	}

	pw, err := os.CreateTemp(filepath.Dir(cfg.PGData), ".doki-postgres-pw-*")
	if err != nil {
		return fmt.Errorf("create PostgreSQL password file: %w", err)
	}
	pwPath := pw.Name()
	defer os.Remove(pwPath)
	if err := pw.Chmod(0600); err != nil {
		_ = pw.Close()
		return fmt.Errorf("chmod PostgreSQL password file: %w", err)
	}
	if _, err := pw.WriteString(cfg.Password + "\n"); err != nil {
		_ = pw.Close()
		return fmt.Errorf("write PostgreSQL password file: %w", err)
	}
	if err := pw.Close(); err != nil {
		return fmt.Errorf("close PostgreSQL password file: %w", err)
	}

	env := providerProcessEnv(paths, cfg.PGData, "", 0, p.effectiveTermuxPrefix())
	if _, err := p.clusterRunner.Run(ctx, "/", "", env, paths.InitDB,
		"-D", cfg.PGData,
		"--username="+cfg.User,
		"--pwfile="+pwPath,
		"--auth-local=trust",
		"--auth-host=scram-sha-256",
	); err != nil {
		return fmt.Errorf("initialize PostgreSQL cluster: %w", err)
	}

	raw, err := os.ReadFile(versionFile)
	if err != nil {
		return fmt.Errorf("PostgreSQL initdb did not create PG_VERSION: %w", err)
	}
	if got := strings.TrimSpace(string(raw)); got != cfg.Major {
		return fmt.Errorf("PostgreSQL initdb created major version %s but image requires %s", got, cfg.Major)
	}

	if cfg.Database != "postgres" {
		db, err := quoteSQLIdentifier(cfg.Database)
		if err != nil {
			return err
		}
		owner, err := quoteSQLIdentifier(cfg.User)
		if err != nil {
			return err
		}
		sql := fmt.Sprintf("CREATE DATABASE %s OWNER %s;\n", db, owner)
		if _, err := p.clusterRunner.Run(ctx, "/", sql, env, paths.Postgres,
			"--single", "-D", cfg.PGData, "postgres",
		); err != nil {
			return fmt.Errorf("create PostgreSQL database %q: %w", cfg.Database, err)
		}
	}
	return nil
}

func quoteSQLIdentifier(value string) (string, error) {
	if value == "" || strings.ContainsAny(value, "\x00\r\n") {
		return "", fmt.Errorf("invalid PostgreSQL identifier %q", value)
	}
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`, nil
}

func providerProcessEnv(paths RuntimePaths, pgdata, host string, port uint16, termuxPrefix string) []string {
	pathValue := paths.BinDir + ":" + filepath.Join(termuxPrefix, "bin") + ":/system/bin"
	env := []string{
		"PATH=" + pathValue,
		"PGDATA=" + pgdata,
		"LD_LIBRARY_PATH=" + filepath.Join(paths.Root, "lib") + ":" + filepath.Join(termuxPrefix, "lib"),
	}
	if host != "" {
		env = append(env, "PGHOST="+host)
	}
	if port != 0 {
		env = append(env, fmt.Sprintf("PGPORT=%d", port))
	}
	return env
}
