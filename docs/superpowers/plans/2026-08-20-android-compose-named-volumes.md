# Android Compose Named Volumes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make Docker named volumes work end-to-end through Doki's Docker API and runtimes so an unchanged Compose file can create, mount, persist, inspect, and remove volumes on Android.

**Architecture:** Move volume ownership out of `pkg/api/server.go` into a focused `pkg/volume.Manager`, using Docker-like `<volume>/volume.json` + `<volume>/_data` storage. Inject the manager into the runtime through a small `VolumeResolver` interface; keep logical volume names in `Config.Mounts` and resolve them only at execution boundaries. Parse Docker `HostConfig.Binds` as either real bind paths or named-volume references and preserve standard lifecycle semantics.

**Tech Stack:** Go 1.27, Doki Docker Engine-compatible API, `common.Mount`, Doki runtime/PRoot, standard `testing`, Docker Compose v5.5.0 for on-device conformance.

**Spec:** `docs/superpowers/specs/2026-08-20-android-docker-compose-compat-design.md`

## Global Constraints

- The existing MiPcTemuco `docker-compose.yml` must remain unchanged.
- Logical named-volume identity must remain the Docker/Compose name; physical Android paths must not replace `Mount.Source` in persisted container config.
- On disk, volume metadata lives at `$DOKI_DATA_DIR/volumes/<name>/volume.json` and user data at `$DOKI_DATA_DIR/volumes/<name>/_data/`.
- `VolumeInfo.Mountpoint` must point to `_data`, never to the metadata directory.
- Existing legacy volumes must migrate deterministically and non-destructively; ambiguous collisions must fail visibly.
- `docker compose down` preserves named volumes; `docker compose down -v` removes them.
- Volumes referenced by stopped containers still count as in use.
- PRoot remains the default Android OCI runtime; this phase does not add PostgreSQL-native provider logic.
- Existing Docker API, Podman volume, runtime, registry, builder, image, and PRoot tests must continue to pass.
- Use TDD for every behavior change: test must fail for the expected reason before implementation.

---

## File Structure

### New files

- `pkg/volume/manager.go` — named-volume storage, validation, migration, metadata persistence, lookup/removal/prune, and physical-path resolution.
- `pkg/volume/manager_test.go` — manager layout, migration, collision, persistence, resolve, remove, and prune tests.
- `pkg/api/mounts.go` — Docker `HostConfig.Binds` parsing/classification and API-side named-volume ensure logic.
- `pkg/api/mounts_test.go` — bind-vs-volume parsing, options, invalid spec, sensitive bind, and auto-create tests.
- `pkg/runtime/volumes.go` — runtime-facing `VolumeResolver` interface and helpers to resolve logical named mounts without mutating persisted config.
- `pkg/runtime/volumes_test.go` — resolver behavior, missing resolver, missing volume, source preservation, and resolved path tests.

### Modified files

- `pkg/api/server.go` — remove embedded `VolumeManager`; consume `*volume.Manager`; route `HostConfig.Binds` through the mount parser; calculate referenced volumes from all containers.
- `pkg/api/server_test.go` — update manager references/imports and add Docker API lifecycle coverage.
- `pkg/podman/api.go` — no interface change is required; `*volume.Manager` must continue satisfying the existing `VolumeStore` interface.
- `pkg/runtime/runtime.go` — add resolver field/option; resolve named volumes in PRoot, namespace, and legacy PRoot mount paths; implement empty-volume image-data copy boundary.
- `cmd/dokid/main.go` — construct one shared volume manager before runtime/server, inject it into both.
- `cmd/doki-compose/main.go` — construct `volume.Manager` and inject it with `WithVolumeResolver` as well, so the standalone compose engine does not regress when runtime starts rejecting unresolved `MountVolume` entries.
- Any tests/call sites that construct `api.NewServer` if the constructor signature changes.

---

### Task 1: Extract the Volume Manager and Introduce `_data` Layout

**Files:**
- Create: `pkg/volume/manager.go`
- Create: `pkg/volume/manager_test.go`
- Modify: `pkg/api/server.go:101-257`
- Modify: `pkg/api/server_test.go:19-78`
- Verify unchanged: `pkg/podman/api.go:30-55` (`VolumeStore` must be satisfied by `*volume.Manager`)

**Interfaces:**
- Produces: `volume.NewManager(root string) (*Manager, error)`
- Produces: `(*Manager).Create(name, driver string, opts, labels map[string]string) (*common.VolumeInfo, error)`
- Produces: `(*Manager).Get(name string) (*common.VolumeInfo, error)`
- Produces: `(*Manager).List() []*common.VolumeInfo`
- Produces: `(*Manager).Remove(name string) error`
- Produces: `(*Manager).Prune(referenced map[string]bool) ([]string, error)`
- Produces: `(*Manager).Resolve(name string) (string, error)` returning `<root>/<name>/_data`
- Consumed later by: API server, runtime `VolumeResolver`, Podman `VolumeStore`

- [ ] **Step 1: Write failing tests for the new on-disk layout**

Create `pkg/volume/manager_test.go` with focused tests equivalent to:

```go
func TestManagerCreateUsesMetadataAndDataDirectories(t *testing.T) {
    root := t.TempDir()
    m, err := NewManager(root)
    if err != nil { t.Fatal(err) }

    v, err := m.Create("compose_data", "", nil, map[string]string{"com.docker.compose.project": "demo"})
    if err != nil { t.Fatal(err) }

    wantData := filepath.Join(root, "compose_data", "_data")
    if v.Mountpoint != wantData {
        t.Fatalf("Mountpoint = %q, want %q", v.Mountpoint, wantData)
    }
    if st, err := os.Stat(wantData); err != nil || !st.IsDir() {
        t.Fatalf("_data missing: %v", err)
    }
    if _, err := os.Stat(filepath.Join(root, "compose_data", "volume.json")); err != nil {
        t.Fatalf("metadata missing: %v", err)
    }
    if _, err := os.Stat(filepath.Join(wantData, "volume.json")); !os.IsNotExist(err) {
        t.Fatalf("metadata leaked into volume data: %v", err)
    }
}
```

Also add `TestManagerResolveReturnsDataDir` and update old API tests so they no longer expect `volume.json` inside `Mountpoint`.

- [ ] **Step 2: Run the tests and verify RED**

Run:

```bash
go test ./pkg/volume ./pkg/api -run 'TestManager(CreateUsesMetadataAndDataDirectories|ResolveReturnsDataDir)|TestVolumeManagerCreateRemove' -count=1
```

Expected: FAIL because `pkg/volume` / `NewManager` do not exist and the current mountpoint is the metadata directory.

- [ ] **Step 3: Move the manager into `pkg/volume` with explicit paths**

Implement helpers in `pkg/volume/manager.go`:

```go
func (m *Manager) volumeDir(name string) string {
    return filepath.Join(m.root, name)
}

func (m *Manager) dataDir(name string) string {
    return filepath.Join(m.volumeDir(name), "_data")
}

func (m *Manager) metadataPath(name string) string {
    return filepath.Join(m.volumeDir(name), "volume.json")
}

func (m *Manager) Resolve(name string) (string, error) {
    v, err := m.Get(name)
    if err != nil { return "", err }
    return v.Mountpoint, nil
}
```

`Create` must create both `volumeDir` and `_data`, set `Mountpoint` to `_data`, and persist metadata at `volumeDir/volume.json`. `Remove` and `Prune` must remove `volumeDir(name)`, not `vol.Mountpoint`.

Persist metadata atomically:

```go
func writeMetadataAtomic(path string, v *common.VolumeInfo) error {
    data, err := json.Marshal(v)
    if err != nil { return err }
    tmp := path + ".tmp"
    if err := os.WriteFile(tmp, data, 0644); err != nil { return err }
    return os.Rename(tmp, path)
}
```

- [ ] **Step 4: Wire API/Podman to the extracted manager**

Change `Server.volumes` to `*volume.Manager`; remove the old manager implementation from `server.go`. Keep `pkg/podman.VolumeStore` as an interface so `*volume.Manager` satisfies it without import cycles.

- [ ] **Step 5: Run focused and package tests**

Run:

```bash
go test ./pkg/volume ./pkg/api ./pkg/podman -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/volume pkg/api/server.go pkg/api/server_test.go
git commit -m "refactor(volume): separate managed volume storage"
```

---

### Task 2: Add Idempotent Legacy-Volume Migration

**Files:**
- Modify: `pkg/volume/manager.go`
- Modify: `pkg/volume/manager_test.go`

**Interfaces:**
- Consumes: `Manager.volumeDir`, `dataDir`, `metadataPath`, `writeMetadataAtomic`
- Produces: internal `migrateLegacyVolume(name string, info *common.VolumeInfo) error`
- Behavior: migration is resumable after any top-level entry has already moved into `_data`

- [ ] **Step 1: Write migration RED tests**

Add:

```go
func TestManagerMigratesLegacyVolumeIntoDataDir(t *testing.T) {
    root := t.TempDir()
    dir := filepath.Join(root, "legacy")
    if err := os.MkdirAll(dir, 0755); err != nil { t.Fatal(err) }
    if err := os.WriteFile(filepath.Join(dir, "PG_VERSION"), []byte("16\n"), 0644); err != nil { t.Fatal(err) }
    old := common.VolumeInfo{Name: "legacy", Driver: "local", Mountpoint: dir}
    raw, _ := json.Marshal(old)
    if err := os.WriteFile(filepath.Join(dir, "volume.json"), raw, 0644); err != nil { t.Fatal(err) }

    m, err := NewManager(root)
    if err != nil { t.Fatal(err) }
    v, err := m.Get("legacy")
    if err != nil { t.Fatal(err) }

    if _, err := os.Stat(filepath.Join(v.Mountpoint, "PG_VERSION")); err != nil {
        t.Fatalf("payload not migrated: %v", err)
    }
    if _, err := os.Stat(filepath.Join(dir, "PG_VERSION")); !os.IsNotExist(err) {
        t.Fatalf("legacy payload still at metadata root: %v", err)
    }
}
```

Add an interrupted/resume test where `_data` already exists with one migrated entry and another legacy entry remains. Add a collision test where both `volumeDir/foo` and `_data/foo` exist; `NewManager` must return an error containing `ambiguous legacy volume migration` and leave both files untouched.

- [ ] **Step 2: Verify RED**

Run:

```bash
go test ./pkg/volume -run 'TestManagerMigratesLegacy|TestManagerResumesLegacy|TestManagerRejectsAmbiguousLegacy' -count=1
```

Expected: FAIL because migration is not implemented.

- [ ] **Step 3: Implement resumable migration**

Algorithm:

```go
func (m *Manager) migrateLegacyVolume(name string, info *common.VolumeInfo) error {
    volDir := m.volumeDir(name)
    dataDir := m.dataDir(name)
    if err := os.MkdirAll(dataDir, 0755); err != nil { return err }

    entries, err := os.ReadDir(volDir)
    if err != nil { return err }
    for _, entry := range entries {
        if entry.Name() == "volume.json" || entry.Name() == "_data" || entry.Name() == "volume.json.tmp" {
            continue
        }
        src := filepath.Join(volDir, entry.Name())
        dst := filepath.Join(dataDir, entry.Name())
        if _, err := os.Lstat(dst); err == nil {
            return fmt.Errorf("ambiguous legacy volume migration %q: both %s and %s exist", name, src, dst)
        } else if !os.IsNotExist(err) {
            return err
        }
        if err := os.Rename(src, dst); err != nil { return err }
    }

    info.Mountpoint = dataDir
    return writeMetadataAtomic(m.metadataPath(name), info)
}
```

Call it from `loadFromDisk` whenever metadata points to the old volume root, is empty, or `_data` is missing while legacy payload exists. Do not migrate metadata whose mountpoint points outside the expected volume directory; reject it as corrupt.

- [ ] **Step 4: Verify GREEN and existing manager tests**

Run:

```bash
go test ./pkg/volume -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/volume/manager.go pkg/volume/manager_test.go
git commit -m "feat(volume): migrate legacy volume layout safely"
```

---

### Task 3: Parse Docker `HostConfig.Binds` as Binds or Named Volumes

**Files:**
- Create: `pkg/api/mounts.go`
- Create: `pkg/api/mounts_test.go`
- Modify: `pkg/api/server.go:999-1035`

**Interfaces:**
- Produces: `parseHostConfigBind(spec string) (common.Mount, error)`
- Produces: `(*Server).ensureNamedVolume(name string) error`
- Consumes: `volume.Manager.Get/Create`
- Invariant: absolute source => `MountBind`; valid non-path source => `MountVolume`

- [ ] **Step 1: Write parser RED tests**

Add table tests:

```go
func TestParseHostConfigBind(t *testing.T) {
    cases := []struct{
        spec string
        typ common.MountType
        src string
        dst string
        ro bool
    }{
        {"mipctemuco_postgres_data:/var/lib/postgresql/data:rw", common.MountVolume, "mipctemuco_postgres_data", "/var/lib/postgresql/data", false},
        {"cache-data:/cache:ro", common.MountVolume, "cache-data", "/cache", true},
        {"/data/local/files:/files:rw", common.MountBind, "/data/local/files", "/files", false},
    }
    // assert every field
}
```

Add invalid cases for empty source/target, relative traversal (`../bad:/data`), malformed option, and non-absolute target.

- [ ] **Step 2: Verify RED**

Run:

```bash
go test ./pkg/api -run 'TestParseHostConfigBind' -count=1
```

Expected: FAIL because helper does not exist.

- [ ] **Step 3: Implement strict classification**

Implement parsing with `strings.SplitN(spec, ":", 3)` for Linux/Android paths. Require absolute container target. If source is absolute, validate `filepath.Clean(source) == source` and retain existing `isSensitiveBindSource` protection. Otherwise validate it as a named volume via exported `volume.ValidName(name)` and return `MountVolume`.

Only `ro` and `rw` are interpreted initially; unknown bind option tokens return a clear `unsupported bind option` error instead of being ignored.

- [ ] **Step 4: Write API RED test for Compose-style auto-create**

Create a Docker create request with:

```json
{
  "Image": "alpine:latest",
  "HostConfig": {
    "Binds": ["demo_data:/var/lib/demo:rw"]
  }
}
```

After the request, assert the persisted `ContainerState.Config.Mounts` contains:

```go
common.Mount{Type: common.MountVolume, Source: "demo_data", Target: "/var/lib/demo"}
```

and `s.volumes.Get("demo_data")` succeeds.

- [ ] **Step 5: Implement API ensure logic**

Replace the current `HostConfig.Binds` loop with:

```go
mnt, err := parseHostConfigBind(bindSpec)
if err != nil {
    s.writeError(w, http.StatusBadRequest, err.Error())
    return
}
if mnt.Type == common.MountVolume {
    if err := s.ensureNamedVolume(mnt.Source); err != nil {
        s.writeError(w, http.StatusInternalServerError, err.Error())
        return
    }
}
cfg.Mounts = append(cfg.Mounts, mnt)
```

`ensureNamedVolume` calls `Get`; on not-found it calls `Create(name, "local", nil, nil)`; a concurrent create conflict is accepted only after `Get` confirms the volume now exists.

Apply the same ensure step to `HostConfig.Mounts` entries whose type is `volume`.

- [ ] **Step 6: Verify API package**

Run:

```bash
go test ./pkg/api ./pkg/volume -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add pkg/api/mounts.go pkg/api/mounts_test.go pkg/api/server.go pkg/volume
git commit -m "feat(api): accept Docker named volume mounts"
```

---

### Task 4: Inject a Volume Resolver into Runtime Without Losing Logical Names

**Files:**
- Create: `pkg/runtime/volumes.go`
- Create: `pkg/runtime/volumes_test.go`
- Modify: `pkg/runtime/runtime.go:60-280`
- Modify: `cmd/dokid/main.go:245-300`
- Modify: `pkg/api/server.go:288-315`
- Modify: call sites/tests of `api.NewServer` as needed

**Interfaces:**
- Produces:

```go
type VolumeResolver interface {
    Resolve(name string) (string, error)
}

func WithVolumeResolver(r VolumeResolver) RuntimeOption
func (rt *Runtime) ResolveMount(m common.Mount) (common.Mount, error)
```

- Consumes: `*volume.Manager`, which satisfies `VolumeResolver`
- Invariant: `ResolveMount` returns a copy; it never mutates persisted `Config.Mounts`

- [ ] **Step 1: Write resolver RED tests**

Use a fake resolver:

```go
type fakeVolumeResolver map[string]string
func (f fakeVolumeResolver) Resolve(name string) (string, error) {
    p, ok := f[name]
    if !ok { return "", common.NewErrNotFound("volume", name) }
    return p, nil
}
```

Test:

```go
func TestRuntimeResolveMountPreservesLogicalSource(t *testing.T) {
    logical := common.Mount{Type: common.MountVolume, Source: "db", Target: "/data"}
    rt := NewRuntime(t.TempDir(), nil, WithVolumeResolver(fakeVolumeResolver{"db": "/host/volumes/db/_data"}))
    resolved, err := rt.ResolveMount(logical)
    if err != nil { t.Fatal(err) }
    if logical.Source != "db" { t.Fatalf("input mutated: %#v", logical) }
    if resolved.Source != "/host/volumes/db/_data" { t.Fatalf("resolved source = %q", resolved.Source) }
    if resolved.Type != common.MountBind { t.Fatalf("resolved type = %q", resolved.Type) }
}
```

Also test missing resolver and unknown volume return explicit errors.

- [ ] **Step 2: Verify RED**

Run:

```bash
go test ./pkg/runtime -run 'TestRuntimeResolveMount' -count=1
```

Expected: FAIL because interfaces/options do not exist.

- [ ] **Step 3: Implement `VolumeResolver` and pure resolution helper**

`ResolveMount` behavior:

```go
func (rt *Runtime) ResolveMount(m common.Mount) (common.Mount, error) {
    if m.Type != common.MountVolume { return m, nil }
    if rt.volumeResolver == nil { return common.Mount{}, fmt.Errorf("named volume %q: no volume resolver configured", m.Source) }
    path, err := rt.volumeResolver.Resolve(m.Source)
    if err != nil { return common.Mount{}, fmt.Errorf("resolve named volume %q: %w", m.Source, err) }
    resolved := m
    resolved.Type = common.MountBind
    resolved.Source = path
    return resolved, nil
}
```

- [ ] **Step 4: Construct one shared manager in `cmd/dokid`**

Before `NewRuntime`, create:

```go
volumeMgr, err := volume.NewManager(filepath.Join(cfg.DataDir, "volumes"))
if err != nil { /* fatal startup error */ }

rt := dr.NewRuntime(execRoot, storeMgr,
    dr.WithRegistry(registry),
    dr.WithDNSAddr(dnsAddr),
    dr.WithVolumeResolver(volumeMgr),
)
```

Change `api.NewServer` to accept `volumeMgr *volume.Manager` as its fifth argument and consume that exact instance; do not let `NewServer` instantiate a second manager with independent state. Update all test call sites. In `cmd/doki-compose/main.go`, create its own manager rooted at `common.VolumeDir()` and inject it into that command's runtime.

- [ ] **Step 5: Compile all constructor call sites**

Run:

```bash
go test ./cmd/dokid ./pkg/api ./pkg/runtime ./pkg/podman -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/runtime/volumes.go pkg/runtime/volumes_test.go pkg/runtime/runtime.go cmd/dokid/main.go pkg/api/server.go pkg/api/server_test.go
git commit -m "feat(runtime): inject named volume resolver"
```

---

### Task 5: Resolve Named Volumes at Every Runtime Mount Boundary

**Files:**
- Modify: `pkg/runtime/runtime.go:1198-1260`
- Modify: `pkg/runtime/runtime.go:1478-1520`
- Modify: `pkg/runtime/runtime.go:2688-2720`
- Modify: `pkg/runtime/lifecycle_test.go`
- Modify: `pkg/runtime/volumes_test.go`

**Interfaces:**
- Consumes: `(*Runtime).ResolveMount(common.Mount)`
- Produces: common resolved-mount iteration used by PRoot, namespace, and legacy PRoot paths

- [ ] **Step 1: Write PRoot mount-argument RED test**

Refactor the PRoot mount-argument construction into this testable helper before changing startup behavior:

```go
func (rt *Runtime) appendProotMountArgs(base []string, rootfs string, mounts []common.Mount) ([]string, error)
```

Test that logical:

```go
common.Mount{Type: common.MountVolume, Source: "db", Target: "/var/lib/data"}
```

with resolver `db -> /host/volumes/db/_data` produces:

```text
-b /host/volumes/db/_data:/var/lib/data
```

while the original mount remains `Source == "db"`.

- [ ] **Step 2: Verify RED**

Run:

```bash
go test ./pkg/runtime -run 'Test.*NamedVolume.*Mount' -count=1
```

Expected: FAIL because `MountVolume` is currently ignored.

- [ ] **Step 3: Resolve before every switch**

At each mount-processing boundary:

```go
for _, logical := range cfg.Mounts {
    mnt, err := rt.ResolveMount(logical)
    if err != nil { return ..., err }
    switch mnt.Type {
    case common.MountBind:
        // existing path
    case common.MountTmpfs:
        // existing path
    }
}
```

Use this in:

1. PRoot startup.
2. namespace/FUSE `setupMounts`.
3. QEMU/PRoot fallback.
4. PRoot `Exec`.
5. PRoot `ExecAttach`.

`Exec` and `ExecAttach` must use one shared `buildProotExecArgs(rootfs, mounts, workingDir, user, command)` helper so an interactive or non-interactive `docker exec` sees exactly the same named-volume bindings as the main container process. Do not persist the resolved physical path back to state.

- [ ] **Step 4: Add namespace and missing-volume tests**

Tests must verify a missing named volume aborts container start with `resolve named volume "db"` rather than silently starting with an empty target.

- [ ] **Step 5: Run runtime regressions**

Run:

```bash
go test ./pkg/runtime ./internal/proot -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/runtime
git commit -m "feat(runtime): mount managed named volumes"
```

---

### Task 6: Implement Initial Copy-to-Volume Semantics

**Files:**
- Modify: `pkg/runtime/volumes.go`
- Modify: `pkg/runtime/volumes_test.go`
- Modify: `pkg/runtime/runtime.go` at container rootfs preparation before process start

**Interfaces:**
- Produces: `(*Runtime).prepareNamedVolumes(rootfs string, mounts []common.Mount) error`
- Consumes: `VolumeResolver`
- Rule: copy image contents only when managed `_data` is empty and `VolumeOptions.NoCopy` is false

- [ ] **Step 1: Write RED tests for empty-volume copy and `NoCopy`**

Construct a rootfs containing `/etc/demo/default.conf` and an empty resolved volume. After `prepareNamedVolumes`, expect `_data/default.conf` to contain the image data.

Add a second test with:

```go
VolumeOptions: &common.VolumeOptions{NoCopy: true}
```

and assert `_data` remains empty.

Add a third test where `_data/existing.txt` already exists; image data must not overwrite or merge into a non-empty volume.

- [ ] **Step 2: Verify RED**

Run:

```bash
go test ./pkg/runtime -run 'TestPrepareNamedVolumes' -count=1
```

Expected: FAIL because helper does not exist.

- [ ] **Step 3: Implement safe copy**

For each `MountVolume`:

1. Resolve `_data` path.
2. Check emptiness with `os.ReadDir`.
3. If non-empty, skip.
4. If `NoCopy`, skip.
5. Resolve `mnt.Target` inside rootfs using `common.SecureJoin`.
6. If target does not exist, leave volume empty.
7. Copy directory contents recursively while preserving modes and symlinks without following symlinks outside the image rootfs.

Use a focused internal helper such as:

```go
func copyVolumeSeed(srcDir, dstDir string) error
```

Reject unsupported special files explicitly rather than copying device nodes.

- [ ] **Step 4: Invoke once after rootfs extraction and before runner start**

Ensure repeated starts are idempotent because a seeded `_data` is no longer empty.

- [ ] **Step 5: Verify runtime tests**

Run:

```bash
go test ./pkg/runtime -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/runtime
git commit -m "feat(volume): seed empty volumes from image data"
```

---

### Task 7: Correct Volume In-Use and Prune Semantics

**Files:**
- Modify: `pkg/api/server.go:259-279, 3141-3205`
- Modify: `pkg/api/server_test.go`

**Interfaces:**
- Produces: `(*Server).referencedVolumeNames() map[string]bool`
- Consumed by: volume delete guard and prune handler
- Rule: created, running, stopped, and exited containers all retain references until container removal

- [ ] **Step 1: Write RED tests for stopped-container references**

Persist a `ContainerState` with `Status: common.StateExited` and:

```go
Mounts: []common.Mount{{Type: common.MountVolume, Source: "db", Target: "/data"}}
```

Assert:

1. `DELETE /volumes/db` without force returns conflict.
2. `POST /volumes/prune` does not delete `db`.

- [ ] **Step 2: Verify RED**

Run:

```bash
go test ./pkg/api -run 'Test.*Volume.*(Stopped|Exited|Prune|InUse)' -count=1
```

Expected: prune test FAIL because current prune only scans running containers.

- [ ] **Step 3: Centralize reference collection**

Implement:

```go
func (s *Server) referencedVolumeNames() map[string]bool {
    refs := map[string]bool{}
    states, err := s.runtime.List()
    if err != nil { return refs }
    for _, st := range states {
        if st.Config == nil { continue }
        for _, m := range st.Config.Mounts {
            if m.Type == common.MountVolume && m.Source != "" {
                refs[m.Source] = true
            }
        }
    }
    return refs
}
```

Use it in prune and `volumeInUse`.

- [ ] **Step 4: Add force-delete behavior assertion**

For this phase, `DELETE /volumes/{name}?force=true` must still return HTTP 409 when any retained container state references the volume. Doki has no safe detach/rewrite mechanism for retained states, so force-delete of referenced backing data is explicitly unsupported rather than destructive. Add an assertion for this exact behavior.

- [ ] **Step 5: Run API + volume tests**

Run:

```bash
go test ./pkg/api ./pkg/volume -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/api/server.go pkg/api/server_test.go
git commit -m "fix(volume): retain references from stopped containers"
```

---

### Task 8: Add Docker API Named-Volume Integration Coverage

**Files:**
- Modify: `pkg/api/server_test.go`
- Create: `pkg/api/volume_integration_test.go`

**Interfaces:**
- Consumes all Phase 1 interfaces
- Produces a daemon/API-level regression proving Docker client semantics rather than only manager internals

- [ ] **Step 1: Write an end-to-end API test**

The test should:

1. Create a temporary Doki config/data directory.
2. Construct shared `volume.Manager` + runtime with `WithVolumeResolver`.
3. Construct `api.Server` with that same manager.
4. POST `/volumes/create` for `demo_data` or let container create auto-provision it.
5. POST `/containers/create?name=demo` with `HostConfig.Binds=["demo_data:/data:rw"]`.
6. Inspect persisted state and volume API.
7. Recreate the manager from disk and assert the volume still resolves to the same `_data` directory.
8. Delete the container, then delete the volume, and verify the whole `<name>` directory is gone.

Use the exact Docker JSON field casing Compose sends.

- [ ] **Step 2: Verify RED if any lifecycle edge remains**

Run only the new test first:

```bash
go test ./pkg/api -run TestDockerAPINamedVolumeLifecycle -count=1 -v
```

Expected before final fixes: FAIL at the first uncovered semantic. Fix only that failing boundary.

- [ ] **Step 3: Run full relevant regression suite**

Run:

```bash
go test ./pkg/volume ./pkg/api ./pkg/podman ./pkg/runtime ./internal/proot ./pkg/common ./pkg/registry ./pkg/image ./pkg/builder -count=1
git diff --check
```

Expected: all PASS and no whitespace errors.

- [ ] **Step 4: Build Android ARM64 daemon**

Run:

```bash
GOOS=android GOARCH=arm64 CGO_ENABLED=0 go build -o /root/doki-build/dokid-android-arm64-named-volumes ./cmd/dokid
sha256sum /root/doki-build/dokid-android-arm64-named-volumes
```

Expected: build exit 0 and a recorded SHA-256.

- [ ] **Step 5: Commit**

```bash
git add pkg/api pkg/volume pkg/runtime cmd/dokid/main.go
git commit -m "test(volume): cover Docker API named volume lifecycle"
```

---

### Task 9: On-Device Docker Compose Conformance for Named Volumes

**Files:**
- Create: `scripts/android/test-compose-named-volumes.sh`
- No changes to MiPcTemuco's `docker-compose.yml`

**Interfaces:**
- Consumes: official Docker Compose v5.5.0 and Doki Docker socket
- Produces: reproducible Android validation before Phase 2 begins

- [ ] **Step 1: Add a reproducible conformance script**

The script must use a temporary Compose project with an ordinary image that can run under PRoot, not PostgreSQL yet. Example YAML generated by the script:

```yaml
services:
  writer:
    image: alpine:latest
    command: ["sh", "-c", "echo persisted > /data/probe.txt && sleep 30"]
    volumes:
      - probe_data:/data
volumes:
  probe_data:
```

The script sets only `DOCKER_HOST` to the Doki socket; it must not use Doki CLI internals for lifecycle operations.

- [ ] **Step 2: Script persistence assertions**

Sequence:

```bash
docker compose up -d
# wait until /data/probe.txt exists via docker compose exec
docker compose down
docker compose up -d
# verify probe.txt still contains "persisted"
docker compose down -v
```

Then query Docker volume API/`docker volume ls` and assert the project volume no longer exists after `down -v`.

- [ ] **Step 3: Verify script syntax locally**

Run:

```bash
bash -n scripts/android/test-compose-named-volumes.sh
```

Expected: exit 0.

- [ ] **Step 4: Install the newly built daemon binary and restart native Doki**

Copy the verified binary into `~/doki-test/bin/` with a versioned filename and update the `dokid` symlink. Because Doki must run in native Termux rather than nested PRoot, stop here for the user's native-Termux restart if a daemon restart is required.

- [ ] **Step 5: Run conformance against the restarted daemon**

Run:

```bash
scripts/android/test-compose-named-volumes.sh
```

Expected: PASS for create, mount, persistence across `down/up`, and deletion via `down -v`.

- [ ] **Step 6: Re-test MiPcTemuco Compose volume creation only**

From `/root/mipctemuco`, run:

```bash
docker compose config
docker compose up -d postgres minio
```

At this phase, PostgreSQL/MinIO process startup may still fail for known independent reasons. The Phase 1 assertion is narrower: Compose must no longer fail with `invalid bind mount source` for `mipctemuco_postgres_data` or `mipctemuco_minio_data`, and both named volumes must exist with `_data` mountpoints.

- [ ] **Step 7: Commit and push Phase 1**

```bash
git add scripts/android/test-compose-named-volumes.sh
git commit -m "test(android): add Compose named volume conformance"
git push origin feat/android-docker-compose
```

Verify:

```bash
LOCAL=$(git rev-parse HEAD)
REMOTE=$(git ls-remote origin refs/heads/feat/android-docker-compose | awk '{print $1}')
test "$LOCAL" = "$REMOTE"
```

Expected: local and remote SHA match exactly.

---

## Phase 1 Completion Gate

Do not begin the Android workload-provider framework until fresh evidence proves all of these:

```text
[ ] pkg/volume tests pass
[ ] pkg/api tests pass
[ ] pkg/podman tests pass
[ ] pkg/runtime tests pass
[ ] internal/proot tests pass
[ ] Android ARM64 dokid build succeeds
[ ] docker compose can auto-create a named volume from HostConfig.Binds
[ ] PRoot receives the resolved _data host path
[ ] docker exec / docker compose exec rebuild PRoot with the same named-volume bindings
[ ] logical volume name remains in persisted ContainerState.Config.Mounts
[ ] volume data survives compose down/up
[ ] compose down -v removes the backing volume directory
[ ] stopped containers protect referenced volumes from prune/removal
[ ] legacy volume migration is idempotent and collision-safe
[ ] MiPcTemuco postgres/minio volume declarations no longer fail bind-path validation
[ ] git diff --check is clean
[ ] feature branch is pushed and local/remote SHA match
```

Only after this gate passes should Phase 2 add the `android-native` provider registry and runner.
