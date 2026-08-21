package postgresql

import (
	"context"
	"crypto/sha256"
	"embed"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

//go:embed patches/*
var postgresPatches embed.FS

type RuntimePaths struct {
	Root      string
	BinDir    string
	Postgres  string
	InitDB    string
	PgIsReady string
	Psql      string
}

type commandRunner interface {
	Run(context.Context, string, string, ...string) ([]byte, error)
}

type sourceBuilder interface {
	Build(context.Context, string, string, string) error
}

type downloadFileFunc func(context.Context, string, string) error

type provisioner struct {
	cacheRoot    string
	termuxPrefix string
	arch         string
	runner       commandRunner
	download     downloadFileFunc
	builder      sourceBuilder
}

type execCommandRunner struct{}

func (execCommandRunner) Run(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

type nativeSourceBuilder struct {
	runner commandRunner
}

func newProvisioner(cacheRoot, termuxPrefix string) *provisioner {
	runner := execCommandRunner{}
	return &provisioner{
		cacheRoot:    cacheRoot,
		termuxPrefix: termuxPrefix,
		arch:         runtime.GOARCH,
		runner:       runner,
		download: func(ctx context.Context, url, dst string) error {
			return downloadFileWithCurl(ctx, runner, termuxPrefix, url, dst)
		},
		builder: &nativeSourceBuilder{runner: runner},
	}
}

func runtimePaths(root string) RuntimePaths {
	bin := filepath.Join(root, "bin")
	return RuntimePaths{
		Root:      root,
		BinDir:    bin,
		Postgres:  filepath.Join(bin, "postgres"),
		InitDB:    filepath.Join(bin, "initdb"),
		PgIsReady: filepath.Join(bin, "pg_isready"),
		Psql:      filepath.Join(bin, "psql"),
	}
}

func (p *provisioner) Ensure(ctx context.Context, contract imageContract) (RuntimePaths, error) {
	if strings.TrimSpace(contract.Version) == "" {
		return RuntimePaths{}, fmt.Errorf("PostgreSQL provider has empty requested version")
	}
	if strings.TrimSpace(p.arch) == "" {
		return RuntimePaths{}, fmt.Errorf("PostgreSQL provider has empty target architecture")
	}

	cached := runtimePaths(filepath.Join(p.cacheRoot, contract.Version, p.arch))
	if ok, err := p.runtimeMatches(ctx, cached, contract.Version); err != nil {
		return RuntimePaths{}, err
	} else if ok {
		return cached, nil
	}

	system := runtimePaths(p.termuxPrefix)
	if ok, err := p.runtimeMatches(ctx, system, contract.Version); err != nil {
		return RuntimePaths{}, err
	} else if ok {
		return system, nil
	}

	if strings.TrimSpace(contract.SHA256) == "" {
		return RuntimePaths{}, fmt.Errorf("PostgreSQL %s source provisioning requires authoritative PG_SHA256", contract.Version)
	}
	if len(contract.SHA256) != 64 {
		return RuntimePaths{}, fmt.Errorf("PostgreSQL %s PG_SHA256 must be 64 hexadecimal characters", contract.Version)
	}

	if err := os.MkdirAll(p.cacheRoot, 0755); err != nil {
		return RuntimePaths{}, fmt.Errorf("create PostgreSQL provider cache: %w", err)
	}
	stageRoot, err := os.MkdirTemp(p.cacheRoot, ".postgresql-"+contract.Version+"-*")
	if err != nil {
		return RuntimePaths{}, fmt.Errorf("create PostgreSQL build staging dir: %w", err)
	}
	defer os.RemoveAll(stageRoot)

	archive := filepath.Join(stageRoot, "postgresql-"+contract.Version+".tar.bz2")
	url := sourceURL(contract.Version)
	if err := p.download(ctx, url, archive); err != nil {
		return RuntimePaths{}, fmt.Errorf("download PostgreSQL %s source: %w", contract.Version, err)
	}
	gotSHA, err := fileSHA256(archive)
	if err != nil {
		return RuntimePaths{}, err
	}
	if !strings.EqualFold(gotSHA, contract.SHA256) {
		return RuntimePaths{}, fmt.Errorf("PostgreSQL %s source SHA256 mismatch: got %s want %s", contract.Version, gotSHA, contract.SHA256)
	}

	installDir := filepath.Join(stageRoot, "install")
	if err := p.builder.Build(ctx, archive, installDir, p.termuxPrefix); err != nil {
		return RuntimePaths{}, fmt.Errorf("build PostgreSQL %s for Android: %w", contract.Version, err)
	}
	built := runtimePaths(installDir)
	if ok, err := p.runtimeMatches(ctx, built, contract.Version); err != nil {
		return RuntimePaths{}, err
	} else if !ok {
		return RuntimePaths{}, fmt.Errorf("built PostgreSQL runtime does not report requested version %s", contract.Version)
	}

	targetParent := filepath.Dir(cached.Root)
	if err := os.MkdirAll(targetParent, 0755); err != nil {
		return RuntimePaths{}, fmt.Errorf("create PostgreSQL version cache: %w", err)
	}
	if _, err := os.Stat(cached.Root); err == nil {
		if ok, matchErr := p.runtimeMatches(ctx, cached, contract.Version); matchErr != nil {
			return RuntimePaths{}, matchErr
		} else if ok {
			return cached, nil
		}
		return RuntimePaths{}, fmt.Errorf("PostgreSQL cache path %s already exists with incompatible contents", cached.Root)
	} else if !os.IsNotExist(err) {
		return RuntimePaths{}, fmt.Errorf("inspect PostgreSQL cache path: %w", err)
	}
	if err := os.Rename(installDir, cached.Root); err != nil {
		return RuntimePaths{}, fmt.Errorf("commit PostgreSQL %s cache: %w", contract.Version, err)
	}
	return cached, nil
}

func (p *provisioner) runtimeMatches(ctx context.Context, paths RuntimePaths, wantVersion string) (bool, error) {
	for _, path := range []string{paths.Postgres, paths.InitDB, paths.PgIsReady, paths.Psql} {
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				return false, nil
			}
			return false, fmt.Errorf("inspect PostgreSQL runtime %s: %w", path, err)
		}
		if info.IsDir() || info.Mode()&0111 == 0 {
			return false, nil
		}
	}
	out, err := p.runner.Run(ctx, "", paths.Postgres, "--version")
	if err != nil {
		return false, nil
	}
	got := parsePostgresVersion(string(out))
	return got == wantVersion, nil
}

func parsePostgresVersion(out string) string {
	fields := strings.Fields(strings.TrimSpace(out))
	if len(fields) == 0 {
		return ""
	}
	return fields[len(fields)-1]
}

func sourceURL(version string) string {
	return "https://ftp.postgresql.org/pub/source/v" + version + "/postgresql-" + version + ".tar.bz2"
}

func downloadFileWithCurl(ctx context.Context, runner commandRunner, termuxPrefix, url, dst string) error {
	curlBin := filepath.Join(termuxPrefix, "bin", "curl")
	info, err := os.Stat(curlBin)
	if err != nil || info.IsDir() || info.Mode()&0111 == 0 {
		return fmt.Errorf("required Termux curl is unavailable: %s", curlBin)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return fmt.Errorf("create PostgreSQL download directory: %w", err)
	}
	_ = os.Remove(dst)
	if _, err := runner.Run(ctx, "", curlBin,
		"-fL",
		"--retry", "3",
		"--retry-delay", "1",
		"-o", dst,
		url,
	); err != nil {
		_ = os.Remove(dst)
		return fmt.Errorf("Termux curl download %s: %w", url, err)
	}
	info, err = os.Stat(dst)
	if err != nil {
		return fmt.Errorf("Termux curl did not create download %s: %w", dst, err)
	}
	if info.Size() == 0 {
		_ = os.Remove(dst)
		return fmt.Errorf("Termux curl created empty download %s", dst)
	}
	return nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open PostgreSQL source for SHA256: %w", err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("hash PostgreSQL source: %w", err)
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

func (b *nativeSourceBuilder) Build(ctx context.Context, archivePath, installDir, termuxPrefix string) error {
	stageRoot := filepath.Dir(installDir)
	srcDir := filepath.Join(stageRoot, "src")
	if err := os.MkdirAll(srcDir, 0755); err != nil {
		return fmt.Errorf("create PostgreSQL source dir: %w", err)
	}

	tarBin := filepath.Join(termuxPrefix, "bin", "tar")
	patchBin := filepath.Join(termuxPrefix, "bin", "patch")
	envBin := filepath.Join(termuxPrefix, "bin", "env")
	makeBin := filepath.Join(termuxPrefix, "bin", "make")
	clangBin := filepath.Join(termuxPrefix, "bin", "clang")
	shBin := filepath.Join(termuxPrefix, "bin", "sh")
	for _, bin := range []string{tarBin, patchBin, envBin, makeBin, clangBin, shBin} {
		if info, err := os.Stat(bin); err != nil || info.IsDir() || info.Mode()&0111 == 0 {
			return fmt.Errorf("required Termux build tool is unavailable: %s", bin)
		}
	}

	if _, err := b.runner.Run(ctx, "", tarBin, "-xjf", archivePath, "-C", srcDir, "--strip-components=1"); err != nil {
		return err
	}
	if err := applyEmbeddedPatches(ctx, b.runner, patchBin, srcDir, stageRoot, termuxPrefix); err != nil {
		return err
	}

	envArgs := []string{
		"PATH=" + filepath.Join(termuxPrefix, "bin") + ":/system/bin",
		"CC=" + clangBin,
		"CPPFLAGS=-I" + filepath.Join(termuxPrefix, "include"),
		"LDFLAGS=-L" + filepath.Join(termuxPrefix, "lib"),
		"PKG_CONFIG_PATH=" + filepath.Join(termuxPrefix, "lib", "pkgconfig") + ":" + filepath.Join(termuxPrefix, "share", "pkgconfig"),
	}
	configure := filepath.Join(srcDir, "configure")
	configureArgs := append([]string{}, envArgs...)
	configureArgs = append(configureArgs,
		shBin, configure,
		"--prefix="+installDir,
		"--with-icu",
		"--with-libxml",
		"--with-openssl",
		"--with-uuid=e2fs",
		"USE_UNNAMED_POSIX_SEMAPHORES=1",
		"pgac_cv_prog_cc_LDFLAGS_EX_BE__Wl___export_dynamic=yes",
		"pgac_cv_prog_cc_LDFLAGS__Wl___as_needed=yes",
	)
	if _, err := b.runner.Run(ctx, srcDir, envBin, configureArgs...); err != nil {
		return err
	}
	makeArgs := append([]string{}, envArgs...)
	makeArgs = append(makeArgs, makeBin, "-j2")
	if _, err := b.runner.Run(ctx, srcDir, envBin, makeArgs...); err != nil {
		return err
	}
	installArgs := append([]string{}, envArgs...)
	installArgs = append(installArgs, makeBin, "install")
	if _, err := b.runner.Run(ctx, srcDir, envBin, installArgs...); err != nil {
		return err
	}
	return nil
}

func applyEmbeddedPatches(ctx context.Context, runner commandRunner, patchBin, srcDir, stageRoot, termuxPrefix string) error {
	entries, err := postgresPatches.ReadDir("patches")
	if err != nil {
		return fmt.Errorf("read embedded PostgreSQL patches: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	patchDir := filepath.Join(stageRoot, "patches")
	if err := os.MkdirAll(patchDir, 0755); err != nil {
		return fmt.Errorf("create PostgreSQL patch dir: %w", err)
	}
	for _, name := range names {
		raw, err := postgresPatches.ReadFile("patches/" + name)
		if err != nil {
			return fmt.Errorf("read embedded PostgreSQL patch %s: %w", name, err)
		}
		patched := strings.ReplaceAll(string(raw), "@TERMUX_PREFIX@", termuxPrefix)
		path := filepath.Join(patchDir, name)
		if err := os.WriteFile(path, []byte(patched), 0644); err != nil {
			return fmt.Errorf("materialize PostgreSQL patch %s: %w", name, err)
		}
		if _, err := runner.Run(ctx, srcDir, patchBin, "-p1", "--batch", "--forward", "-i", path); err != nil {
			return fmt.Errorf("apply PostgreSQL Android patch %s: %w", name, err)
		}
	}
	return nil
}
