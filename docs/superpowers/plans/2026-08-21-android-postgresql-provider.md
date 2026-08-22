# Android PostgreSQL Provider Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Run the unchanged official `postgres:16-alpine` Compose service on Android through Doki using an exact PostgreSQL 16.15 Android-native runtime, persistent named-volume data, normal Docker lifecycle/health semantics, and a generic TCP port proxy for `5750:5432`.

**Architecture:** Register a concrete PostgreSQL implementation of the existing `AndroidWorkloadProvider` interface. The provider recognizes only the official PostgreSQL image family with authoritative OCI `PG_VERSION`/`PG_SHA256`, provisions an exact Android-native build under Doki's provider cache, translates the official image environment/command contract into native `initdb`/`postgres`/`pg_isready`/`psql` execution, and never substitutes the installed Termux PostgreSQL 18.2 for requested 16.15. Published ports are handled by a generic runtime-owned TCP proxy so PostgreSQL still listens on its container-private port 5432 at a deterministic per-container loopback address while host port 5750 remains a Docker publication.

**Tech Stack:** Go 1.27, Doki runtime/provider framework, Termux Android ARM64, PostgreSQL 16.15 source release, Docker Engine-compatible API, Docker Compose v5, standard `testing`.

**Spec:** `docs/superpowers/specs/2026-08-20-android-docker-compose-compat-design.md`

## Global Constraints

- Work directly on `feat/android-docker-compose`; do not create a new branch or worktree.
- Do not use agents/subagents for execution in this session.
- `dokid` runs natively in Termux; never start/restart it from Debian/PRoot.
- TDD is mandatory: RED -> root-cause confirmation -> minimal GREEN -> regression.
- The official image metadata currently verified on-device is `PG_MAJOR=16`, `PG_VERSION=16.15`, `PG_SHA256=c1575341fa7bd40f5274ea465b34390f4dc64cdd0770af327005caaeb9f6b7ed`.
- The same SHA is freshly verified against `https://ftp.postgresql.org/pub/source/v16.15/postgresql-16.15.tar.bz2`.
- Native Termux currently provides PostgreSQL 18.2. It is not compatible with the requested major/version policy and must never be silently reused for `postgres:16-alpine`.
- Provider cache root is `$DOKI_DATA_DIR/providers/postgresql/<version>/<arch>/`.
- Provider matching is allowlisted and deterministic; no PostgreSQL branching is added to Docker API handlers.
- Unsupported official-image environment/options are rejected explicitly, never ignored.
- Named volume logical `Source` must remain unchanged in persisted `Config`; provider execution uses the resolved `_data` `HostPath`.
- PostgreSQL must keep private port 5432. Published port 5750 is implemented as a proxy, not by changing PostgreSQL's server port to 5750.
- Provider exec and healthchecks must use binaries from the same exact provisioned PostgreSQL version as the server.
- Existing legacy provider/PRoot behavior must remain unchanged when PostgreSQL does not match.
- Phase 3 does not implement MinIO, Docker-socket aliasing, Codex builds, or full Compose service networking.

## File Structure

### New files

- `pkg/runtime/providers/postgresql/provider.go` — official-image matching, provider construction, Prepare/PrepareExec/Cleanup orchestration.
- `pkg/runtime/providers/postgresql/provider_test.go` — matching, exact version contract, mount/env/command behavior.
- `pkg/runtime/providers/postgresql/provisioner.go` — exact-version cache/system/source resolution with verified source digest.
- `pkg/runtime/providers/postgresql/provisioner_test.go` — cache hit, exact system version reuse, mismatch rejection/source plan, checksum errors.
- `pkg/runtime/providers/postgresql/cluster.go` — PGDATA validation, idempotent initialization, auth/database setup command construction.
- `pkg/runtime/providers/postgresql/cluster_test.go` — empty/existing cluster, required env, unsupported options, command construction.
- `pkg/runtime/portproxy.go` — generic runtime-owned TCP forwarding lifecycle.
- `pkg/runtime/portproxy_test.go` — real loopback echo forwarding, collision/error, close/restart behavior.
- `scripts/android/test-postgresql-provider.sh` — on-device Docker Compose conformance for unchanged `postgres:16-alpine` semantics.

### Modified files

- `pkg/runtime/android_provider.go` — add `ContainerID` to `WorkloadDescriptor`; add generic `PortForward` declarations to `PreparedWorkload`.
- `pkg/runtime/android_native.go` — start runtime-owned proxies after provider process startup and roll back on proxy failure.
- `pkg/runtime/runtime.go` — keep proxy handles per container and close them when the process exits / is deleted.
- `pkg/runtime/android_provider_test.go` and `pkg/runtime/android_native_test.go` — regression coverage for descriptor ID and proxy lifecycle.
- `cmd/dokid/main.go` — construct/register PostgreSQL provider using Doki data dir and Termux prefix; fail startup if registration itself is invalid.
- `docs/superpowers/plans/2026-08-21-android-postgresql-provider.md` — append final verification record only after fresh gates.

---

### Task 1: Match Official PostgreSQL Images and Extract an Exact Runtime Contract

**Files:**
- Create: `pkg/runtime/providers/postgresql/provider.go`
- Create: `pkg/runtime/providers/postgresql/provider_test.go`
- Modify: `pkg/runtime/android_provider.go`
- Modify: `pkg/runtime/android_provider_test.go`

**Interfaces:**
- Produces: `type Provider struct { ... }`
- Produces: `func New(cacheRoot, termuxPrefix string, opts ...Option) *Provider`
- Produces: `func (p *Provider) ID() string` returning `postgresql`
- Produces internal `type imageContract struct { Version, Major, SHA256 string }`
- Consumes: `runtime.WorkloadDescriptor`

- [ ] **Step 1: Write RED tests for descriptor container identity**

Add `ContainerID string` expectations to `descriptorFromConfig`: a config ID such as `pg-one` must appear unchanged in the provider descriptor and mutating the descriptor must not mutate config.

Run:

```bash
/root/go1.27/bin/go test ./pkg/runtime -run 'TestDescriptorFromConfig' -count=1
```

Expected: FAIL until `ContainerID` is added and populated.

- [ ] **Step 2: Implement the minimal descriptor field and verify GREEN**

Add:

```go
type WorkloadDescriptor struct {
    ContainerID string
    // existing fields unchanged
}
```

and in `descriptorFromConfig`:

```go
ContainerID: cfg.ID,
```

Re-run the targeted test and `git diff --check`.

- [ ] **Step 3: Write RED provider matching tests**

Tests must cover:

```text
postgres:16-alpine + PG_VERSION=16.15 + PG_MAJOR=16 + PG_SHA256=<verified> -> Matched=true Required=true
docker.io/library/postgres:16-alpine with same metadata -> required match
library/postgres@sha256:... with same metadata -> required match
postgres-shaped tag without PG_VERSION/PG_SHA256 -> explicit required match followed by Ensure compatibility error, not legacy fallback
mycorp/postgres:16 -> no match
postgresql:16 -> no match
postgres:16 with PG_MAJOR inconsistent with PG_VERSION -> compatibility error
```

The provider should only claim official repository identities (`postgres`, `library/postgres`, `docker.io/library/postgres`, equivalent normalized Docker Hub forms), not arbitrary repositories ending in `/postgres`.

Run:

```bash
/root/go1.27/bin/go test ./pkg/runtime/providers/postgresql -run 'TestProviderMatch|TestImageContract' -count=1 -v
```

Expected: compile failure because the provider package does not exist.

- [ ] **Step 4: Implement matching + contract extraction minimally**

Use image config env, not user env, for authoritative version metadata:

```go
PG_MAJOR
PG_VERSION
PG_SHA256
```

`Match` recognizes official identity and returns `ProviderMatch{Matched:true, Required:true, Reason:"official PostgreSQL image requires Android-native runtime"}`. `Ensure` calls contract parsing and returns actionable errors such as:

```text
PostgreSQL image postgres:16-alpine is missing authoritative PG_VERSION
PostgreSQL image requests PG_VERSION 16.15 but PG_MAJOR is 17
```

Do not provision yet.

- [ ] **Step 5: Verify GREEN and commit**

Run:

```bash
/root/go1.27/bin/go test ./pkg/runtime/providers/postgresql ./pkg/runtime -run 'TestProviderMatch|TestImageContract|TestDescriptorFromConfig' -count=1
git diff --check
```

Commit:

```bash
git add pkg/runtime/android_provider.go pkg/runtime/android_provider_test.go pkg/runtime/providers/postgresql/provider.go pkg/runtime/providers/postgresql/provider_test.go
git commit -m "feat(runtime): match Android PostgreSQL workloads"
```

---

### Task 2: Provision the Exact PostgreSQL Version with Verified Source Inputs

**Files:**
- Create: `pkg/runtime/providers/postgresql/provisioner.go`
- Create: `pkg/runtime/providers/postgresql/provisioner_test.go`
- Modify: `pkg/runtime/providers/postgresql/provider.go`

**Interfaces:**
- Produces:

```go
type RuntimePaths struct {
    Root      string
    BinDir    string
    Postgres  string
    InitDB    string
    PgIsReady string
    Psql      string
}

type commandRunner interface {
    Run(context.Context, string, ...string) ([]byte, error)
}

func (p *provisioner) Ensure(context.Context, imageContract) (RuntimePaths, error)
```

- [ ] **Step 1: Write RED exact-version tests**

Cover these behaviors with a fake runner/filesystem fixture:

1. Cache `$cacheRoot/16.15/arm64/bin/postgres` reports `postgres (PostgreSQL) 16.15` -> reuse without download/build.
2. Termux `$PREFIX/bin/postgres` reports 16.15 -> reuse exact system runtime.
3. Termux reports 18.2 while request is 16.15 -> do not reuse; source provisioning path is selected.
4. Source URL is exactly `https://ftp.postgresql.org/pub/source/v16.15/postgresql-16.15.tar.bz2`.
5. Downloaded bytes whose SHA differs from `PG_SHA256` fail before extraction/build.
6. Missing `PG_SHA256` fails source provisioning rather than downloading unverified input.

Run and observe RED.

- [ ] **Step 2: Implement exact cache/system detection**

Parse `postgres --version` as exact `major.minor[.patch]` text and compare the full requested `PG_VERSION`, not only `PG_MAJOR`.

The existing Termux 18.2 must therefore be rejected for 16.15.

- [ ] **Step 3: Implement verified source provisioning**

Use native Termux tools via absolute `$PREFIX/bin/...` paths because provisioning runs inside native `dokid`. Build in a temporary cache sibling and atomically rename on success.

Commands are equivalent to:

```bash
curl -fL "$URL" -o "$TMP/postgresql.tar.bz2"
sha256sum "$TMP/postgresql.tar.bz2"
tar -xjf "$TMP/postgresql.tar.bz2" -C "$TMP/src" --strip-components=1
cd "$TMP/src"
./configure --prefix="$TMP/install" --without-readline --without-zlib --without-icu --without-openssl
make -j2
make install
"$TMP/install/bin/postgres" --version
```

The Go code itself computes SHA-256 before extraction; external `sha256sum` is not the trust boundary. Require the built binary to report the exact requested version before atomically moving `install` into `$cacheRoot/<version>/<arch>`.

- [ ] **Step 4: Verify GREEN and commit**

Run:

```bash
/root/go1.27/bin/go test ./pkg/runtime/providers/postgresql -run 'TestProvision' -count=1 -v
git diff --check
```

Commit:

```bash
git add pkg/runtime/providers/postgresql/provisioner.go pkg/runtime/providers/postgresql/provisioner_test.go pkg/runtime/providers/postgresql/provider.go
git commit -m "feat(runtime): provision exact Android PostgreSQL versions"
```

---

### Task 3: Translate the Official Entrypoint Environment and Initialize PGDATA Idempotently

**Files:**
- Create: `pkg/runtime/providers/postgresql/cluster.go`
- Create: `pkg/runtime/providers/postgresql/cluster_test.go`
- Modify: `pkg/runtime/providers/postgresql/provider.go`
- Modify: `pkg/runtime/providers/postgresql/provider_test.go`

**Interfaces:**
- Produces internal:

```go
type clusterConfig struct {
    PGData   string
    User     string
    Password string
    Database string
}

func clusterConfigFromDescriptor(runtime.WorkloadDescriptor) (clusterConfig, error)
func (p *Provider) ensureCluster(context.Context, RuntimePaths, clusterConfig) error
```

- [ ] **Step 1: Write RED environment/mount validation tests**

Required accepted surface:

```text
POSTGRES_USER
POSTGRES_PASSWORD
POSTGRES_DB
PGDATA
```

User env overrides image defaults. `PGDATA` defaults to image `PGDATA`, currently `/var/lib/postgresql/data`.

Resolve PGDATA only through a matching provider mount whose logical target contains PGDATA; use that mount's `HostPath`.

Explicitly reject, if present:

```text
POSTGRES_INITDB_ARGS
POSTGRES_HOST_AUTH_METHOD
POSTGRES_INITDB_WALDIR
any non-empty /docker-entrypoint-initdb.d contract advertised through a future descriptor field
```

For this phase, `POSTGRES_PASSWORD` is required when initializing a new cluster. Existing initialized clusters may restart without re-running initdb.

- [ ] **Step 2: Write RED initialization tests**

Use a fake command runner and temporary data dir:

- empty PGDATA -> exactly one `initdb`, then create requested database if it differs from `postgres`;
- directory containing `PG_VERSION` matching major 16 -> no initdb;
- existing cluster `PG_VERSION=17` for requested 16.15 -> error;
- partial non-empty directory without `PG_VERSION` -> error, never overwrite;
- password is supplied via a temporary `0600` pwfile, never command-line args or persisted provider state.

Database creation after `initdb` uses native single-user mode so no temporary background daemon is required:

```text
postgres --single -D <PGDATA> postgres
```

with SQL equivalent to:

```sql
CREATE DATABASE "techservice" OWNER "techservice";
```

Identifiers must be quoted safely and validated against NUL/newline injection.

- [ ] **Step 3: Implement minimal initialization**

`initdb` arguments:

```text
-D <host PGDATA>
--username=<POSTGRES_USER>
--pwfile=<temp file>
--auth-local=trust
--auth-host=scram-sha-256
```

Initialization is considered complete only when `PG_VERSION` exists and matches the requested major.

- [ ] **Step 4: Verify GREEN and commit**

Run provider tests and `git diff --check`, then commit:

```bash
git add pkg/runtime/providers/postgresql/cluster.go pkg/runtime/providers/postgresql/cluster_test.go pkg/runtime/providers/postgresql/provider.go pkg/runtime/providers/postgresql/provider_test.go
git commit -m "feat(runtime): initialize Android PostgreSQL clusters"
```

---

### Task 4: Prepare Server and Exec Commands from the Same Exact Runtime

**Files:**
- Modify: `pkg/runtime/providers/postgresql/provider.go`
- Modify: `pkg/runtime/providers/postgresql/provider_test.go`

**Interfaces:**
- `Provider.Prepare` returns exact cached `postgres` executable.
- `Provider.PrepareExec` maps PostgreSQL utilities to exact cached binaries.

- [ ] **Step 1: Write RED Prepare tests**

For the MiPcTemuco contract, assert `Prepare` returns:

```text
Executable: <16.15 runtime>/bin/postgres
Args:       command override after removing official docker-entrypoint wrapper semantics
Env:        provider runtime env including PGDATA host path and endpoint
Cwd:        PGDATA or /
```

Default image command `[postgres]` must not become an argument `postgres postgres`; it means launch the selected `postgres` binary with no extra server args.

- [ ] **Step 2: Write RED exec/health tests**

`PrepareExec` supports allowlisted commands from the same runtime:

```text
pg_isready
psql
postgres
```

For `CMD-SHELL` healthcheck input eventually passed as `sh -c ...`, preserve shell semantics using Termux `/system/bin/sh` or `$PREFIX/bin/sh`, but prepend provider runtime `bin` to `PATH` and set `PGHOST`/`PGPORT` so unchanged `pg_isready -U techservice -d techservice` reaches the provider endpoint.

Reject unknown absolute host executables and attempts to escape the allowlisted command mapping.

- [ ] **Step 3: Implement and verify GREEN**

Run:

```bash
/root/go1.27/bin/go test ./pkg/runtime/providers/postgresql -run 'TestProviderPrepare|TestProviderPrepareExec' -count=1 -v
```

Commit:

```bash
git add pkg/runtime/providers/postgresql/provider.go pkg/runtime/providers/postgresql/provider_test.go
git commit -m "feat(runtime): prepare Android PostgreSQL processes"
```

---

### Task 5: Add Generic Runtime-Owned TCP Port Forwarding

**Files:**
- Create: `pkg/runtime/portproxy.go`
- Create: `pkg/runtime/portproxy_test.go`
- Modify: `pkg/runtime/android_provider.go`
- Modify: `pkg/runtime/android_native.go`
- Modify: `pkg/runtime/runtime.go`
- Modify: `pkg/runtime/android_native_test.go`

**Interfaces:**
- Produces:

```go
type PortForward struct {
    ListenHost string
    ListenPort uint16
    TargetHost string
    TargetPort uint16
}

type PreparedWorkload struct {
    Executable   string
    Args         []string
    Env          []string
    Cwd          string
    PortForwards []PortForward
}
```

- Produces runtime helpers:

```go
func startTCPForward(spec PortForward) (io.Closer, error)
func (rt *Runtime) replacePortProxies(containerID string, specs []PortForward) error
func (rt *Runtime) closePortProxies(containerID string)
```

- [ ] **Step 1: Write RED real-socket proxy test**

Start a real temporary TCP echo server on loopback, create a `PortForward` with an ephemeral/public test listener, connect through the proxy, send bytes, and assert exact round-trip. Also assert `Close` releases the listening port.

- [ ] **Step 2: Implement minimal TCP proxy**

Accept connections, dial target, copy both directions, close both sides deterministically. No UDP in Phase 3.

- [ ] **Step 3: Write RED runtime lifecycle tests**

A fake Android provider returns one forward. Assert:

- successful Start owns proxy;
- proxy listen failure causes Start to fail and kills/reaps the just-started provider process;
- process exit closes proxy;
- Stop/Kill eventually close proxy;
- Start after exited recreates proxy.

- [ ] **Step 4: Integrate proxy lifecycle minimally**

Add a proxy map/mutex to `Runtime`. Start proxies only after provider process start succeeds. `monitorProcess` closes them before/while final state becomes exited. `Delete` closes any residual handles.

- [ ] **Step 5: Verify runtime regression and commit**

Run:

```bash
/root/go1.27/bin/go test ./pkg/runtime -run 'TestTCPForward|TestAndroidNative.*Port' -count=1 -v
/root/go1.27/bin/go test ./pkg/runtime -count=1
git diff --check
```

Commit:

```bash
git add pkg/runtime/portproxy.go pkg/runtime/portproxy_test.go pkg/runtime/android_provider.go pkg/runtime/android_native.go pkg/runtime/runtime.go pkg/runtime/android_native_test.go
git commit -m "feat(runtime): proxy Android provider ports"
```

---

### Task 6: Give PostgreSQL a Deterministic Private Endpoint and Published Port Plan

**Files:**
- Modify: `pkg/runtime/providers/postgresql/provider.go`
- Modify: `pkg/runtime/providers/postgresql/provider_test.go`

**Interfaces:**
- Produces internal `func endpointForContainer(id string) net.IP`
- Consumes `WorkloadDescriptor.ContainerID` and `WorkloadDescriptor.Ports`.

- [ ] **Step 1: Write RED endpoint/port tests**

Derive a deterministic loopback address from container ID within `127.64.0.0/10`, avoiding `.0`/`.255` host bytes. Same ID -> same address; distinct fixture IDs -> distinct addresses.

For `5750:5432/tcp`, server args must include:

```text
-h <private-loopback>
-p 5432
```

and `PreparedWorkload.PortForwards` must contain:

```text
ListenHost: requested Port.IP, default 0.0.0.0
ListenPort: 5750
TargetHost: <private-loopback>
TargetPort: 5432
```

Reject UDP publications for this PostgreSQL provider in Phase 3.

- [ ] **Step 2: Implement minimal planner and verify GREEN**

Provider exec env includes:

```text
PGHOST=<private-loopback>
PGPORT=5432
```

so `pg_isready` and `psql` target the private endpoint while inspection still reports Docker's original `5750:5432` mapping.

- [ ] **Step 3: Commit**

```bash
git add pkg/runtime/providers/postgresql/provider.go pkg/runtime/providers/postgresql/provider_test.go
git commit -m "feat(runtime): plan PostgreSQL private endpoints"
```

---

### Task 7: Register the PostgreSQL Provider in Native dokid

**Files:**
- Modify: `cmd/dokid/main.go`
- Add/modify: `cmd/dokid/main_test.go` only if a small provider-registration helper is extracted.

**Interfaces:**
- Consumes `postgresql.New(filepath.Join(dataDir,"providers","postgresql"), termuxPrefix)`.

- [ ] **Step 1: Write RED registration test/helper test**

Extract a helper only if needed to make registration testable without starting the daemon. An empty/non-Android environment must not claim PostgreSQL unless the provider is explicitly registered by `dokid`; no runtime package global registration.

- [ ] **Step 2: Register provider**

In `main.go` after `androidProviders := dr.NewAndroidProviderRegistry()`:

```go
pgProvider := postgresql.New(
    filepath.Join(dataDir, "providers", "postgresql"),
    common.TermuxPrefix(),
)
if err := androidProviders.Register(pgProvider); err != nil {
    logger.Error("register PostgreSQL Android provider", "err", err)
    os.Exit(1)
}
```

If `common.TermuxPrefix()` does not exist, add a small common helper that resolves `PREFIX` with safe Android default `/data/data/com.termux/files/usr`; test it separately.

- [ ] **Step 3: Verify repository tests and commit**

Run:

```bash
/root/go1.27/bin/go test ./cmd/dokid ./pkg/runtime/providers/postgresql ./pkg/runtime -count=1
git diff --check
```

Commit:

```bash
git add cmd/dokid/main.go cmd/dokid/main_test.go pkg/common
git commit -m "feat(dokid): register Android PostgreSQL provider"
```

Stage only files that actually changed.

---

### Task 8: Add On-Device PostgreSQL Conformance and Close Phase 3 Gate

**Files:**
- Create: `scripts/android/test-postgresql-provider.sh`
- Modify: `docs/superpowers/plans/2026-08-21-android-postgresql-provider.md`

**Conformance contract:** unchanged Compose service semantics for `postgres:16-alpine` with user/database/password `techservice`, published `5750:5432`, named volume, and `pg_isready` healthcheck.

- [x] **Step 1: Write the conformance script before installing a new daemon**

The script creates a temporary Compose project using exactly:

```yaml
services:
  postgres:
    image: postgres:16-alpine
    environment:
      POSTGRES_DB: techservice
      POSTGRES_USER: techservice
      POSTGRES_PASSWORD: techservice
    ports:
      - "5750:5432"
    volumes:
      - postgres_data:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U techservice -d techservice"]
      interval: 2s
      timeout: 2s
      retries: 20
volumes:
  postgres_data:
```

The script must verify:

1. container reaches healthy;
2. `docker compose exec -T postgres postgres --version` reports `16.15` (or exact provider requested version output, never 18.2);
3. host TCP connection through `127.0.0.1:5750` accepts SQL via exact provider `psql` or Compose exec;
4. create table/row, `down`, `up`, row still exists;
5. `down -v` removes volume;
6. container inspect retains `5432/tcp` private and host `5750` publication.

- [x] **Step 2: Run full pre-build test gate**

```bash
/root/go1.27/bin/go test ./... -count=1
git diff --check
```

Both must pass freshly.

- [x] **Step 3: Build Android ARM64 daemon**

```bash
mkdir -p /root/doki-build
GOOS=android GOARCH=arm64 CGO_ENABLED=0 /root/go1.27/bin/go build \
  -o /root/doki-build/dokid-android-arm64-postgresql-provider ./cmd/dokid
sha256sum /root/doki-build/dokid-android-arm64-postgresql-provider
```

- [x] **Step 4: Install a versioned copy and self-reexec native dokid safely**

Copy/build to a versioned path under:

```text
/data/data/com.termux/files/home/doki-test/bin/dokid-android-arm64.<versioned-name>
```

Update only the `~/doki-test/bin/dokid` symlink. Verify the exact active PID, command line, and `/proc/<pid>/exe` before signaling anything. Then send `SIGUSR2`: native `dokid` re-execs itself in place. Verify that the PID is unchanged and `/proc/<pid>/exe` now resolves to the new versioned binary. Do not use a blind process restart.

- [x] **Step 5: Verify the self-reexeced daemon and run on-device conformance**

Verify PID, `/proc/<pid>/exe`, exact SHA, `/_ping`, and `/version`, then run:

```bash
scripts/android/test-compose-named-volumes.sh
scripts/android/test-postgresql-provider.sh
```

If PostgreSQL source provisioning is triggered, inspect progress/logs and do not treat MCP transport timeout as build failure; determine status from processes/cache/logs.

- [x] **Step 6: Verify real MiPcTemuco PostgreSQL service unchanged**

Using `/root/mipctemuco/docker-compose.yml`, target only PostgreSQL first without editing YAML:

```bash
docker compose -f /root/mipctemuco/docker-compose.yml up -d postgres
docker compose -f /root/mipctemuco/docker-compose.yml ps postgres
docker compose -f /root/mipctemuco/docker-compose.yml exec -T postgres postgres --version
docker compose -f /root/mipctemuco/docker-compose.yml exec -T postgres pg_isready -U techservice -d techservice
```

Confirm host port 5750 accepts a real SQL query and named-volume data survives recreate.

- [ ] **Step 7: Append verification record, commit, push, compare SHA**

Record exact PostgreSQL version, source SHA, daemon SHA, conformance results, and any source-build duration-independent evidence. Then:

```bash
git add scripts/android/test-postgresql-provider.sh docs/superpowers/plans/2026-08-21-android-postgresql-provider.md
git commit -m "test(android): verify PostgreSQL native provider"
git push origin feat/android-docker-compose
LOCAL=$(git rev-parse HEAD)
REMOTE=$(git ls-remote origin refs/heads/feat/android-docker-compose | awk '{print $1}')
test "$LOCAL" = "$REMOTE"
```

Do not begin Phase 4 until the local/remote SHA match and all Phase 3 gates above are freshly green.

## Phase 3 pre-push verification record — 2026-08-22 UTC

Phase 3 functional verification was completed on the physical Android `android-freecad` node. Phase 4 / MinIO was not started. Step 7 remains open until the verification commit is pushed and local/remote branch SHAs match.

### Exact PostgreSQL provider

- Compose image contract: `postgres:16-alpine`.
- Image metadata: `PG_MAJOR=16`, `PG_VERSION=16.15`, `PG_SHA256=c1575341fa7bd40f5274ea465b34390f4dc64cdd0770af327005caaeb9f6b7ed`.
- Source URL: `https://ftp.postgresql.org/pub/source/v16.15/postgresql-16.15.tar.bz2`.
- Exact provider runtime: PostgreSQL `16.15`.
- Runtime executable: `/data/data/com.termux/files/usr/var/lib/doki/providers/postgresql/16.15/arm64/bin/postgres`.
- The Termux PostgreSQL 18.2 executable was not substituted.

### Functional gate daemon

The final code-bearing functional gate used:

- code commit: `e7f25e7` (`fix(api): honor Docker container filters`);
- native `dokid` PID: `22100`;
- executable: `/data/data/com.termux/files/home/doki-test/bin/dokid-android-arm64.20260821-phase3-containerfilters-v15`;
- SHA256: `01bbeaf34942a6667e78e4c57e4b14b4193376ff57e2bfd40979d62a0f4bb79d`;
- `/version`: Doki `0.12.0`, API `1.55`, GitCommit `e7f25e7`, Go `1.27.0`, `android/arm64`;
- `/_ping`: `OK`;
- deployment: verified symlink update followed by `SIGUSR2` self-reexec, with PID remaining `22100`.

### Fresh automated verification

- `/root/go1.27/bin/go test ./... -count=1` — exit `0`.
- `git diff --check` — clean.
- `PROJECT=volconformancephase3e7f25e7 scripts/android/test-compose-named-volumes.sh` — exit `0`.
- `PROJECT=pgproviderconformancephase3e7f25e7 scripts/android/test-postgresql-provider.sh` — exit `0`.

The PostgreSQL conformance verified: healthy state; PostgreSQL 16.15; inspect publication `5432/tcp -> 5750`; exact provider process executable; Compose exec SQL; external SQL via `127.0.0.1:5750`; stop/start; restart; logs; port close/reopen lifecycle; persistence across `down/up` and `--force-recreate`; `down -v` volume removal; and a fresh empty database after recreation following `down -v`.

### Unchanged MiPcTemuco Compose verification

Using `/root/mipctemuco/docker-compose.yml` unchanged and targeting only the existing `postgres` service:

- health reached `healthy`;
- `postgres --version` returned PostgreSQL `16.15`;
- live `/proc/<pid>/exe` resolved to the provider 16.15 executable;
- Compose SQL returned `techservice|techservice`;
- external SQL through host port `5750` returned `techservice|techservice`;
- a reversible smoke row survived `docker compose up -d --force-recreate postgres`;
- the smoke table was removed afterward;
- the Docker map/bool project filter fix removed false cross-project `orphan containers` warnings;
- `docker compose down` left zero MiPcTemuco PostgreSQL container states, closed port `5750`, and preserved `mipctemuco_postgres_data`.

### Lifecycle regressions discovered during the real Compose gate

Two additional runtime defects were reproduced with RED tests and fixed before this verification record:

1. explicit `Stop` now suppresses `restart: unless-stopped` automatic restart until a later explicit `Start`;
2. `monitorProcess` no longer recreates deleted container state after a delete/exit race.

Docker container list filters were also corrected to accept Docker's map/bool filter encoding so Compose project isolation works correctly.
