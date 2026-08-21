# Android Docker Compose Compatibility Design

Date: 2026-08-20
Branch: `feat/android-docker-compose`
Target: Doki on Termux/Android ARM64
Reference workload: the unmodified `docker-compose.yml` from MiPcTemuco

## 1. Goal

Doki must allow an ordinary Docker Compose project to run on Android without Android-specific edits to the project's Compose file.

The primary acceptance command is:

```bash
cd <compose-project>
docker compose up -d --build
```

The same Compose file should continue to work with Docker Engine on a normal Linux host. Android-specific behavior belongs in Doki, not in application repositories.

For the reference MiPcTemuco project, the existing Compose file must remain unchanged while Doki supports:

- `postgres:16-alpine` with a persistent named volume and healthcheck.
- `minio/minio:latest` with its persistent named volume.
- multi-stage `codex-runtime-image` builds.
- `codex-runtime-manager`, including its `/var/run/docker.sock` bind.
- standard Compose lifecycle operations (`up`, `ps`, `logs`, `stop`, `start`, `restart`, `down`, and `down -v`).

The design prioritizes Docker semantics over merely making a process start. Doki must not silently substitute incompatible software versions or silently ignore unsupported volume, healthcheck, networking, or lifecycle behavior.

## 2. Constraints and facts established on Android

The current work has already demonstrated the following:

1. Docker Compose v5.5.0 can talk to Doki's Docker-compatible Unix socket.
2. Docker Hub pulls work after Android DNS and Docker Hub reference normalization fixes.
3. Docker Compose image inspection, labels, `all=1`, standard `/build` TAR bodies, build args, targets, and several OCI extraction cases are now supported by the feature branch.
4. Docker image `ENTRYPOINT` + `CMD` resolution was incorrect and is fixed on the feature branch.
5. PRoot on Android can execute many Linux ARM64 userspaces, but the official Linux PostgreSQL image cannot complete `initdb` reliably. The failure was traced to `postgres --check` entering `ptrace_stop` while PRoot and its `--shm-helper` wait on each other during System V IPC handling.
6. Passing `--sysvipc` to PRoot is necessary but does not resolve the PostgreSQL deadlock.
7. PostgreSQL built by Termux works on the same Android device. The Termux package is compiled for Android-specific primitives (including unnamed POSIX semaphores), confirming that the Linux image's runtime assumptions are the incompatibility rather than Compose itself.
8. Doki's named-volume API exists, but the Docker Compose path is incomplete: Compose commonly sends named volumes through `HostConfig.Binds`, while current Doki code treats every bind source as an absolute host path. Runtime mount handling also does not yet resolve `MountVolume` to managed storage.
9. Doki's `ModeNative` executes host programs directly and therefore is not by itself an OCI image runner. A provider layer is required if an OCI workload must be represented by an Android-native process while preserving Docker lifecycle semantics.

These facts rule out treating PostgreSQL as one more PRoot tweak. The design introduces an explicit Android workload-provider mechanism while keeping PRoot as the default for compatible Linux workloads.

## 3. Architectural principles

### 3.1 Compose remains the source of intent

Doki receives the same Docker API requests that Docker Engine would receive. It must interpret those requests without requiring Compose extensions, Android-specific labels, alternate YAML files, or wrapper commands.

Application repositories stay platform-agnostic.

### 3.2 Preserve semantic identity

An adapter must not silently turn one requested product/version into another. For example, a pulled `postgres:16-alpine` image must not become PostgreSQL 18 simply because Termux currently packages PostgreSQL 18.

The PostgreSQL provider will read the pulled OCI image metadata, including `PG_VERSION` when present, and provision a matching Android-native PostgreSQL version. If the requested version cannot be provided, container creation/start must fail with an actionable compatibility error.

### 3.3 Adapters are registered providers, not API special cases

There must be no scattered logic such as:

```go
if image == "postgres:16-alpine" { ... }
```

Instead, Android-native compatibility is a subsystem with a registry and provider interface. PostgreSQL is the first provider. Future providers may support other workloads that fundamentally cannot run under PRoot.

### 3.4 PRoot remains the normal Linux workload path

A Linux OCI image that works under PRoot continues to use PRoot. The Android-native provider path is selected only when a registered provider explicitly claims the workload and its compatibility requirements justify native execution.

This keeps the special path small and avoids turning Doki into a collection of host-process substitutions.

### 3.5 Unsupported behavior fails visibly

If a provider cannot preserve a requested semantic (version, environment option, volume behavior, network behavior, user, healthcheck, or command), Doki must return a clear unsupported-feature error rather than pretending the container is healthy.

## 4. High-level architecture

```text
Docker Compose v5
      |
      v
Docker Engine-compatible API
      |
      +--> image / build / volume / network managers
      |
      v
Runtime planning
      |
      +--> normal Linux OCI workload ------> PRoot runner
      |
      +--> foreign architecture -----------> QEMU/FEX where available
      |
      +--> Android-native compatible ------> Android workload runner
                                                |
                                                v
                                      WorkloadProviderRegistry
                                                |
                                                +--> PostgreSQL provider
                                                +--> future providers
```

Selection happens inside runtime planning, after image metadata and container configuration are known. The Docker API handler does not contain product-specific branching.

## 5. Android workload provider subsystem

### 5.1 New execution mode

Introduce a dedicated execution mode such as `android-native` rather than overloading the existing `native` runner.

`ModeNative` currently means direct host execution and assumes the requested executable already exists on the host. `android-native` has different semantics: it represents an OCI workload through a provider that translates the OCI/container contract into an Android-native implementation.

The mode is available only on Android.

### 5.2 Provider contract

The exact Go names may vary during implementation, but the abstraction should provide the following responsibilities:

```go
type AndroidWorkloadProvider interface {
    ID() string
    Match(ctx context.Context, desc WorkloadDescriptor) MatchResult
    Ensure(ctx context.Context, desc WorkloadDescriptor) error
    Prepare(ctx context.Context, container *Config) (*PreparedWorkload, error)
    Start(ctx context.Context, prepared *PreparedWorkload) (*Process, error)
    Exec(ctx context.Context, prepared *PreparedWorkload, exec *ExecConfig) (*Process, error)
    Stop(ctx context.Context, prepared *PreparedWorkload, signal syscall.Signal) error
    Remove(ctx context.Context, prepared *PreparedWorkload) error
}
```

`WorkloadDescriptor` contains normalized image identity, OCI config, architecture/platform, environment, command/entrypoint, mounts, ports, labels, and relevant Docker HostConfig fields.

Providers are registered with a `WorkloadProviderRegistry`. `Match` returns both whether the provider applies and why. Ambiguous matches are an error.

### 5.3 Provider selection

Runtime planning follows this order on Android:

1. Respect an explicit Docker runtime request if present and valid.
2. Handle WASM/foreign-architecture cases as today.
3. Query Android workload providers.
4. If exactly one provider matches and reports that native adaptation is required, select `android-native`.
5. If that matched provider cannot satisfy the requested semantic contract (for example the exact PostgreSQL version), fail explicitly; do not fall back to PRoot for a workload already classified as fundamentally incompatible.
6. If no provider claims the workload, use the normal highest-priority compatible runner, normally PRoot on Android.

Provider matching must be deterministic and testable. The match is centralized in the provider registry, not distributed through API handlers. The OCI image is still pulled/inspected first so the provider decision is tied to an immutable image digest and its metadata rather than only to user-supplied tag text.

### 5.4 Lifecycle integration

The Android workload runner must integrate with the same persisted `ContainerState` and lifecycle machinery as other runners:

- container ID/name and Compose labels remain normal Doki state;
- stdout/stderr are written to the normal Doki container log path;
- PID, exit code, timestamps, restart count, health status, and stop signal are maintained by Doki;
- `docker compose ps`, `logs`, `stop`, `start`, `restart`, and `down` use the normal Docker API;
- healthchecks execute through provider `Exec` when the container uses the Android-native runner.

Provider-specific state is stored under the container state directory in a versioned internal record, not exposed as Docker API schema.

## 6. PostgreSQL Android provider

### 6.1 Matching

The first provider supports the official PostgreSQL image family. Matching uses normalized OCI image identity plus image metadata, not a literal single tag. At minimum it must recognize official `postgres` references and validate the expected PostgreSQL metadata.

The provider reads `PG_MAJOR`, `PG_VERSION`, and `PG_SHA256` from the pulled image configuration when available. `PG_VERSION` is the authoritative requested runtime version for provisioning.

### 6.2 Version provisioning

The provider must preserve the requested PostgreSQL version.

Provisioning order:

1. Reuse a previously cached Android-native build of the exact requested version.
2. Reuse a compatible system/Termux binary only if its PostgreSQL version exactly satisfies the requested version policy.
3. Otherwise provision/build the requested PostgreSQL source for Android using the verified version/checksum from OCI metadata and cache it under Doki's data directory.

A version mismatch is never silently accepted.

Provider artifacts live below a Doki-managed path such as:

```text
$DOKI_DATA_DIR/providers/postgresql/<version>/<arch>/
```

The provisioning layer should be separable from lifecycle execution so prebuilt release artifacts can be introduced later without changing container semantics.

### 6.3 Official-entrypoint compatibility

The provider translates the subset of the official PostgreSQL image contract needed for normal Compose operation. The initial supported surface includes:

- `POSTGRES_USER`
- `POSTGRES_PASSWORD`
- `POSTGRES_DB`
- `PGDATA`
- normal image `CMD`/command override for starting PostgreSQL
- stop signal
- persistent data directory
- `pg_isready` healthchecks

Additional official-image options such as `POSTGRES_INITDB_ARGS`, `POSTGRES_HOST_AUTH_METHOD`, and `/docker-entrypoint-initdb.d` must either be implemented with tests or rejected explicitly. They must not be silently ignored.

Initialization is idempotent: an empty data volume is initialized once; an existing valid cluster is reused.

### 6.4 Exec and healthchecks

Provider `Exec` resolves PostgreSQL utilities such as `pg_isready` and `psql` from the same provisioned version used by the server. This allows Docker healthcheck commands to remain unchanged.

Health status remains owned by Doki's existing healthcheck supervisor so Compose conditions such as `service_healthy` work normally.

### 6.5 Networking and ports

The provider must preserve Docker's distinction between container-private and published ports. It must not simply reinterpret `5750:5432` as "make PostgreSQL think its internal port is 5750".

For the initial Android-native runner, Doki will maintain a per-container loopback endpoint and a user-space port proxy for published ports. The PostgreSQL process may bind a Doki-assigned local endpoint, while Docker API inspection continues to report private port `5432` and public port `5750` as requested.

The port-forwarding component must be generic so other Android-native providers can reuse it.

## 7. Named volumes

Named volumes are required before the PostgreSQL provider is considered usable.

### 7.1 On-disk layout

Use a Docker-like separation between metadata and user data:

```text
$DOKI_DATA_DIR/volumes/<name>/
    volume.json
    _data/
```

`VolumeInfo.Mountpoint` points to `_data`, never to the directory containing `volume.json`. This prevents volume metadata from appearing inside application filesystems such as PostgreSQL `PGDATA`.

### 7.2 Docker API parsing

`HostConfig.Binds` must distinguish:

- absolute source path -> bind mount;
- valid non-path source -> named volume.

Thus Compose input such as:

```text
mipctemuco_postgres_data:/var/lib/postgresql/data:rw
```

becomes a `MountVolume`, while:

```text
/home/user/data:/data:rw
```

remains `MountBind`.

`HostConfig.Mounts` with `Type=volume` follows the same managed-volume path.

Missing named volumes are created according to Docker/Compose semantics when appropriate.

### 7.3 Runtime resolution

A mount must retain its logical Docker identity (`Name`/volume source) while runners receive a resolved physical mountpoint.

Do not overwrite the logical `Source` with a host path. Introduce a volume-resolution boundary shared by runners, for example a `VolumeResolver` injected into `Runtime`. The resolver maps a logical volume name to its `_data` path at start/exec time.

This preserves:

- correct Docker inspect output;
- volume-in-use checks;
- prune/remove behavior;
- persistence across daemon restarts;
- runner independence from API internals.

### 7.4 Lifecycle semantics

Required behavior:

- `docker compose up` creates missing named volumes.
- container removal does not remove named volumes.
- `docker compose down` preserves them.
- `docker compose down -v` removes Compose-owned named volumes.
- an in-use volume cannot be removed without force semantics where supported.
- `docker volume inspect` reports `_data` as the mountpoint.
- volume state survives a `dokid` restart.

Docker's initial copy-to-volume behavior should be implemented for a newly created empty volume unless `NoCopy` is requested. This should be tested independently from PostgreSQL.

## 8. Docker socket alias on Android

The existing Compose file binds:

```text
/var/run/docker.sock:/var/run/docker.sock
```

Doki's actual Termux socket is under the Termux prefix. Requiring an Android-specific YAML path would violate the design goal.

Doki therefore treats `/var/run/docker.sock` as a canonical Docker-socket alias when it is the bind source and the daemon is running on Android. It resolves that alias only to Doki's own configured Unix socket.

Important constraints:

- the alias is exact; arbitrary `/var/run/*` binds are not rewritten;
- existing sensitive-bind protections remain in place for other paths;
- the guest destination remains `/var/run/docker.sock`;
- permissions and read/write mode follow the requested bind;
- tests verify that the alias cannot be used to expose another host socket or path.

This lets Docker clients inside `codex-runtime-manager` communicate with Doki through the standard Docker socket path.

## 9. MinIO and ordinary Linux workloads

MinIO should remain an OCI/PRoot workload unless a fundamental Android incompatibility is proven.

The previous MinIO failure occurred during rootfs extraction. Upstream `main` now contains broader TAR extraction fixes, and the feature branch is rebased on those fixes. MinIO must therefore be retested with a newly built daemon before any provider is considered.

The same principle applies to Node/Codex images: prefer the normal OCI build + PRoot execution path. Android-native providers are a compatibility escape hatch for fundamental runtime mismatches, not the default.

## 10. Build compatibility

The existing feature branch already adds standard Docker `/build` TAR-body support, Docker build args/labels/target parsing, and builder extraction fixes.

The remaining acceptance work for MiPcTemuco is to verify the real multi-stage `codex-runtime-manager/Dockerfile`, including:

- `FROM node:20-bookworm-slim`;
- package-manager `RUN` steps;
- `npm ci`;
- target selection (`runtime` and `manager`);
- stage-to-stage copies;
- final image records and tags.

A build that only creates metadata without running the declared commands is not considered success.

## 11. Networking

Networking is staged by required behavior rather than by attempting full Docker bridge parity immediately.

For the reference Compose project, Doki must provide:

1. published host ports exactly as requested;
2. service-to-service reachability where Compose dependencies need it;
3. stable service-name resolution on the Compose network;
4. host access needed by configured `host.docker.internal` entries;
5. no accidental exposure of services that have no published ports.

For PRoot workloads this may use Doki's existing network manager plus user-space forwarding. Android-native providers use the same generic forwarding/name-resolution layer rather than inventing provider-specific networking.

If a requested network mode cannot be represented safely on Android, Doki returns an explicit compatibility error.

## 12. Error handling and observability

Every compatibility decision must be diagnosable.

Add structured fields to daemon logs for:

- selected runner;
- selected Android provider, if any;
- provider match reason;
- resolved volume names and mountpoint IDs (without leaking sensitive data);
- Docker socket alias resolution;
- port proxy creation;
- provider provisioning version and cache hit/miss;
- unsupported provider option errors.

Container-facing errors should explain which Docker/Compose feature could not be represented and suggest a Doki capability/doctor command where useful.

The compatibility layer must never report a container `Up` if its provider process failed to initialize.

## 13. Security model

Android-native providers intentionally have less filesystem isolation than PRoot containers. Doki must expose that fact honestly in runtime metadata/logging.

Requirements:

- providers receive only declared environment, mounts, and ports;
- provider data and binaries remain under Doki-managed/Termux-private paths;
- volume names are validated against traversal;
- Docker socket aliasing can target only the daemon's own socket;
- no automatic arbitrary host executable mapping from image commands;
- provider matching and provisioning are allowlisted through registered providers;
- downloaded/prebuilt provider artifacts require digest verification;
- source-built provider inputs require version/checksum verification when metadata provides it.

## 14. Compatibility and migration

The feature branch is based on current `upstream/main`. Existing generic fixes should remain separable into upstreamable commits.

Named-volume layout migration must account for older Doki volumes that stored `volume.json` in the volume root. Migration must be deterministic and non-destructive:

1. detect old layout;
2. create `_data`;
3. move only user payload entries into `_data` while keeping metadata outside;
4. update persisted mountpoint metadata;
5. make the operation idempotent and test interrupted migration recovery.

No automatic destructive conversion is allowed if the layout is ambiguous.

## 15. Testing strategy

### 15.1 Unit tests

Add focused tests for:

- named-volume parsing from `HostConfig.Binds`;
- bind vs volume discrimination;
- volume `_data` layout and migration;
- volume resolution in each runner that supports volumes;
- `down`/remove/prune/in-use semantics at manager level;
- provider registry matching and ambiguity;
- exact PostgreSQL version resolution;
- unsupported PostgreSQL environment/options;
- provider lifecycle state transitions;
- provider healthcheck exec;
- Docker socket alias security;
- port mapping planner.

Existing package tests and `git diff --check` remain mandatory.

### 15.2 Integration tests

Add temporary-data-dir daemon tests using the real Docker API surface for:

- volume create/inspect/remove;
- container create with named volume;
- daemon restart with volume/container metadata retained;
- Android-provider planning using fake providers so CI does not need Termux;
- socket alias resolution using a temporary Unix socket;
- health status transitions.

### 15.3 On-device Android conformance

Maintain a reproducible script that runs against the native Termux daemon and official Docker Compose binary. The reference sequence is:

```bash
docker compose config
docker compose up -d --build
docker compose ps -a
docker compose logs --no-color
# service health checks
docker compose restart
docker compose down
docker compose up -d
docker compose down -v
```

The script verifies persisted data before `down -v` and verifies removal after `down -v`.

Transport timeouts from MCP tooling are not treated as command failure; daemon/process/log state is inspected independently.

## 16. Implementation phases

### Phase 1 — Managed named volumes

Implement correct Docker named-volume parsing, `_data` storage, runtime resolution, inspect/lifecycle semantics, and tests. Validate with a simple Alpine container before PostgreSQL.

### Phase 2 — Android workload-provider framework

Add `android-native` execution mode, provider registry, deterministic selection, persisted provider state, lifecycle/log/exec hooks, and fake-provider tests. No PostgreSQL product logic belongs in the Docker API layer.

### Phase 3 — PostgreSQL provider

Implement exact-version provisioning, initialization, volume use, lifecycle, `pg_isready` healthcheck execution, and generic port forwarding. Validate `postgres:16-alpine` from the unchanged Compose definition.

### Phase 4 — MinIO and OCI runtime closure

Rebuild Doki on current upstream base and retest `minio/minio:latest`. Fix only generic OCI/PRoot defects if encountered. Confirm MinIO data persistence through its named volume.

### Phase 5 — Docker socket alias and Codex manager

Implement secure `/var/run/docker.sock` aliasing, complete real multi-stage Codex builds, start `codex-runtime-manager`, and verify its `/health` plus its Docker API access to Doki.

### Phase 6 — Full reference Compose conformance

Run the unmodified MiPcTemuco Compose file end to end. The base acceptance target is the default service set produced by plain `docker compose up -d --build`; profile-gated services such as `service-closure-worker` are a follow-up conformance pass using their existing Compose profile, still without YAML modifications. Fix remaining generic networking, restart-policy, healthcheck, inspect, and lifecycle incompatibilities and add the resulting cases to Doki's compatibility suite.

## 17. Acceptance criteria

The work is complete for the reference project only when all of the following are true on the Android device:

1. The project Compose file has no Android-specific modifications.
2. `docker compose config` succeeds.
3. `docker compose up -d --build` exits successfully.
4. `docker compose ps -a` reports expected services with correct lifecycle/health state.
5. PostgreSQL requested as `postgres:16-alpine` runs a compatible PostgreSQL 16.x runtime, not another major version.
6. PostgreSQL accepts real SQL connections and its healthcheck becomes healthy.
7. PostgreSQL data survives container recreation and `docker compose down`/`up`.
8. `docker compose down -v` removes the Compose-owned PostgreSQL and MinIO volumes.
9. MinIO responds on its API/console endpoints and persists data.
10. Codex runtime images are built from the declared Dockerfile stages, not stubbed.
11. `codex-runtime-manager` is healthy and can use `/var/run/docker.sock` to talk to Doki.
12. `docker compose logs`, `stop`, `start`, and `restart` operate through standard Docker API semantics.
13. All new unit/integration tests pass and the Android conformance run is captured in logs.
14. Existing Doki tests continue to pass.
15. After base acceptance passes, `docker compose --profile workers up -d` is validated separately for the existing profile-gated worker services without Android-specific YAML changes.

## 18. Explicit non-goals for this effort

This project does not claim complete Docker Engine parity on Android. It does not require implementing kernel features Android cannot provide, full privileged-container semantics, cgroup parity, or arbitrary Linux daemon compatibility through native providers.

The immediate objective is a principled compatibility architecture plus enough generic Docker/Compose behavior to run the reference Compose project unchanged. New provider types are added only when a fundamental runtime incompatibility is demonstrated.
