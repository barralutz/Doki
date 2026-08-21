# Android Native Provider Framework Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a generic `android-native` execution mode and provider registry so Doki can represent selected OCI workloads with Android-native host processes while preserving Docker container lifecycle/state semantics.

**Architecture:** Keep the existing global PRoot/namespace/native execution paths unchanged for normal containers. Add a separate Android workload-provider registry that can claim a container at create-time; persist that decision per container as `ModeAndroidNative` plus a versioned provider-state record. At start/exec time, the provider translates the OCI/container contract into an absolute Android host executable plus args/env/cwd; Doki still owns PID, logs, restart policy, healthchecks, stop/kill, and Docker API state.

**Tech Stack:** Go 1.27, Doki runtime state machine, Docker-compatible API, Termux/Android ARM64, standard `testing`.

**Spec:** `docs/superpowers/specs/2026-08-20-android-docker-compose-compat-design.md`

## Global Constraints

- This phase adds no PostgreSQL-specific matching or provisioning logic.
- Existing PRoot, namespace, native, build, image, Compose, and volume behavior must remain unchanged when no provider claims a workload.
- `ModeAndroidNative` must be appended to the execution-mode enum; existing numeric mode values 0-11 must not change because they are persisted in `state.json`.
- An Android provider must be explicitly registered; arbitrary image commands must never map directly to arbitrary host executables.
- Provider selection is deterministic: zero required matches means legacy execution; exactly one required match means `android-native`; more than one required match is an error.
- A provider that claims a workload and later cannot satisfy its contract must return an explicit error; Doki must not silently fall back to PRoot.
- Provider decisions must survive daemon restarts independently of in-memory registry order.
- Provider-specific persisted state lives at `$DOKI_DATA_DIR/runtimes/containers/<id>/android-provider.json`, separate from Docker-facing `state.json`.
- Provider state contains no environment-variable values or other duplicated secrets.
- Prepared host executables must be absolute paths.
- Named-volume logical identities remain in `Config.Mounts`; provider preparation receives resolved host paths separately and must not mutate persisted mounts.
- Docker healthchecks must execute through the same Android provider as the main process.
- Use TDD for every behavior change and keep commits independently reviewable.

---

## File Structure

### New files

- `pkg/runtime/android_provider.go` — provider interfaces, workload descriptor types, prepared process types, match result, provider registry, and descriptor construction helpers.
- `pkg/runtime/android_provider_test.go` — registry determinism, ambiguity, duplicate IDs, descriptor immutability, explicit provider selection, and no-provider fallback tests.
- `pkg/runtime/android_provider_state.go` — versioned provider decision persistence (`android-provider.json`).
- `pkg/runtime/android_provider_state_test.go` — atomic save/load, version validation, no-secret persistence, and missing/corrupt state tests.
- `pkg/runtime/android_native.go` — generic Android-native start/exec process bridge using registered providers.
- `pkg/runtime/android_native_test.go` — fake-provider start/exec/lifecycle tests.
- `pkg/runtime/android_provider_integration_test.go` — per-container mode selection and daemon-restart-style reload tests with a fake provider.

### Modified files

- `pkg/runtime/runner.go` — append `ModeAndroidNative`; update string/parser/info/all-mode surfaces.
- `pkg/runtime/runner_test.go` — numeric stability plus `android-native` parser/info tests.
- `pkg/runtime/runtime.go` — inject provider registry, choose mode during `Create`, skip OCI rootfs extraction for adapted containers, honor `state.Mode` for Start/Exec/ExecAttach, and call provider cleanup on delete.
- `pkg/api/server.go` — no product-specific branching; only ensure inspect/runtime reporting continues to expose selected mode through existing state paths where already supported.
- `cmd/dokid/main.go` — construct an empty `AndroidProviderRegistry` and inject it into `Runtime`; Phase 3 will register PostgreSQL into this registry.

---

### Task 1: Append `ModeAndroidNative` Without Changing Existing Numeric Modes

**Files:**
- Modify: `pkg/runtime/runner.go`
- Modify: `pkg/runtime/runner_test.go`

**Interfaces:**
- Produces: `ModeAndroidNative ExecutionMode`
- Produces: `ParseExecutionMode("android-native")`
- Consumed by: provider planning, persisted `ContainerState.Mode`, runtime lifecycle switches

- [ ] **Step 1: Write RED tests for numeric stability and new parsing**

Add tests equivalent to:

```go
func TestExecutionModeNumericValuesRemainStable(t *testing.T) {
    want := map[ExecutionMode]int{
        ModeNative: 0, ModeProot: 1, ModeNamespaces: 2, ModeMicroVM: 3,
        ModeGVisor: 4, ModeWASM: 5, ModePkDroid: 6, ModeSysbox: 7,
        ModeQEMUUser: 8, ModeChroot: 9, ModeFEX: 10, ModeLegacy32: 11,
    }
    for mode, n := range want {
        if int(mode) != n { t.Fatalf("%s = %d, want %d", mode, mode, n) }
    }
}

func TestAndroidNativeExecutionMode(t *testing.T) {
    if int(ModeAndroidNative) != 12 { t.Fatalf("ModeAndroidNative = %d", ModeAndroidNative) }
    if ModeAndroidNative.String() != "android-native" { t.Fatal("wrong mode string") }
    got, ok := ParseExecutionMode("android-native")
    if !ok || got != ModeAndroidNative { t.Fatalf("parse = %v %v", got, ok) }
}
```

Update `TestAllExecutionModes` expected count from 12 to 13 and assert the new mode appears exactly once.

- [ ] **Step 2: Run RED**

Run:

```bash
go test ./pkg/runtime -run 'TestExecutionModeNumericValuesRemainStable|TestAndroidNativeExecutionMode|TestAllExecutionModes' -count=1
```

Expected: compile/test failure because `ModeAndroidNative` does not exist.

- [ ] **Step 3: Append the mode**

Append after `ModeLegacy32`:

```go
ModeAndroidNative // 12: registered Android-native workload provider
```

Add info:

```go
ExecutionModeInfo{
    Mode: ModeAndroidNative,
    Name: "android-native",
    Level: 1,
    Isolation: "android-host-process",
    Platforms: []string{"android/arm64", "android/armv7"},
    Description: "registered Android-native workload provider managed through Docker semantics",
}
```

Add it to `String`, `ParseExecutionMode`, `AllExecutionModes`, and `ExecutionModeInfos` without reordering existing numeric constants.

- [ ] **Step 4: Verify GREEN**

Run:

```bash
go test ./pkg/runtime -run 'TestExecutionMode|TestParseExecutionMode|TestAllExecutionModes|TestAndroidNativeExecutionMode' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/runtime/runner.go pkg/runtime/runner_test.go
git commit -m "feat(runtime): add android-native execution mode"
```

---

### Task 2: Define the Android Workload Provider Contract and Registry

**Files:**
- Create: `pkg/runtime/android_provider.go`
- Create: `pkg/runtime/android_provider_test.go`

**Interfaces:**
- Produces:

```go
type WorkloadMount struct {
    Mount    common.Mount
    HostPath string
}

type WorkloadDescriptor struct {
    ImageRef     string
    ImageDigest  string
    Platform     string
    ImageConfig  *ImageOCIConfig
    Args         []string
    Env          []string
    Cwd          string
    User         string
    Labels       map[string]string
    Mounts       []WorkloadMount
    Ports        []common.Port
    HealthCheck  *HealthCheckConfig
    StopSignal   string
}

type ProviderMatch struct {
    Matched  bool
    Required bool
    Reason   string
}

type PreparedWorkload struct {
    Executable string
    Args       []string
    Env        []string
    Cwd        string
}

type PreparedExec struct {
    Executable string
    Args       []string
    Env        []string
    Cwd        string
}

type AndroidWorkloadProvider interface {
    ID() string
    Match(context.Context, WorkloadDescriptor) ProviderMatch
    Ensure(context.Context, WorkloadDescriptor) error
    Prepare(context.Context, WorkloadDescriptor) (*PreparedWorkload, error)
    PrepareExec(context.Context, WorkloadDescriptor, *ExecConfig) (*PreparedExec, error)
    Cleanup(context.Context, WorkloadDescriptor) error
}
```

- Produces:

```go
type AndroidProviderRegistry struct { ... }
func NewAndroidProviderRegistry() *AndroidProviderRegistry
func (r *AndroidProviderRegistry) Register(p AndroidWorkloadProvider) error
func (r *AndroidProviderRegistry) Get(id string) AndroidWorkloadProvider
func (r *AndroidProviderRegistry) SelectRequired(ctx context.Context, desc WorkloadDescriptor) (*ProviderSelection, error)
```

- Consumed by: runtime create/start/exec/delete planning

- [ ] **Step 1: Write registry RED tests**

Create fake providers and tests for:

```go
func TestAndroidProviderRegistrySelectRequired(t *testing.T) {
    reg := NewAndroidProviderRegistry()
    reg.Register(&fakeProvider{id: "one", match: ProviderMatch{Matched:true, Required:true, Reason:"needs android"}})
    sel, err := reg.SelectRequired(context.Background(), WorkloadDescriptor{ImageRef:"example:1"})
    if err != nil { t.Fatal(err) }
    if sel.Provider.ID() != "one" || sel.Match.Reason != "needs android" { t.Fatalf("selection=%+v", sel) }
}
```

Also test:

- no matches -> `(nil, nil)`;
- `Matched=true, Required=false` -> no selection;
- two required matches -> error contains `multiple Android providers require workload` and both provider IDs;
- duplicate provider ID -> `Register` returns error;
- empty provider ID -> error;
- registry selection order does not change the unique selected provider.

- [ ] **Step 2: Verify RED**

Run:

```bash
go test ./pkg/runtime -run 'TestAndroidProviderRegistry' -count=1
```

Expected: FAIL because provider types/registry do not exist.

- [ ] **Step 3: Implement the provider types and registry**

Use a mutex-protected `map[string]AndroidWorkloadProvider` plus sorted IDs when evaluating providers so ambiguity errors are deterministic.

`SelectRequired` must evaluate every provider, collect only `Matched && Required`, and:

```go
switch len(required) {
case 0:
    return nil, nil
case 1:
    return &ProviderSelection{Provider: required[0].provider, Match: required[0].match}, nil
default:
    return nil, fmt.Errorf("multiple Android providers require workload: %s", strings.Join(ids, ", "))
}
```

- [ ] **Step 4: Add descriptor immutability helpers**

Implement:

```go
func descriptorFromConfig(cfg *Config, mounts []WorkloadMount) WorkloadDescriptor
```

It copies slice/map fields instead of returning aliases to `cfg`. Test that mutating descriptor args/env/labels/mounts does not mutate persisted config inputs.

- [ ] **Step 5: Verify GREEN**

Run:

```bash
go test ./pkg/runtime -run 'TestAndroidProviderRegistry|TestDescriptorFromConfig' -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/runtime/android_provider.go pkg/runtime/android_provider_test.go
git commit -m "feat(runtime): add Android workload provider registry"
```

---

### Task 3: Persist the Provider Decision Separately from Docker State

**Files:**
- Create: `pkg/runtime/android_provider_state.go`
- Create: `pkg/runtime/android_provider_state_test.go`

**Interfaces:**
- Produces:

```go
const androidProviderStateVersion = 1

type AndroidProviderState struct {
    Version     int    `json:"version"`
    ProviderID  string `json:"providerId"`
    MatchReason string `json:"matchReason,omitempty"`
}

func (rt *Runtime) saveAndroidProviderState(containerID string, state AndroidProviderState) error
func (rt *Runtime) loadAndroidProviderState(containerID string) (*AndroidProviderState, error)
```

- Consumed by: create, start, exec, cleanup

- [ ] **Step 1: Write RED persistence tests**

Test that save writes exactly:

```text
<runtime-root>/containers/<id>/android-provider.json
```

and reload returns the same `ProviderID`/reason/version.

Add tests that:

- missing file returns a wrapped not-found error;
- corrupt JSON returns an explicit parse error;
- version `2` returns `unsupported Android provider state version 2`;
- serialized JSON contains no config env values supplied elsewhere in the test.

- [ ] **Step 2: Verify RED**

Run:

```bash
go test ./pkg/runtime -run 'TestAndroidProviderState' -count=1
```

Expected: FAIL because persistence helpers do not exist.

- [ ] **Step 3: Implement atomic state writes**

Use the same temp-file + `Sync` + rename pattern as `saveState`, but write only provider ID, version, and non-sensitive match reason.

Reject empty provider IDs on save/load.

- [ ] **Step 4: Verify GREEN**

Run:

```bash
go test ./pkg/runtime -run 'TestAndroidProviderState' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/runtime/android_provider_state.go pkg/runtime/android_provider_state_test.go
git commit -m "feat(runtime): persist Android provider decisions"
```

---

### Task 4: Select `android-native` Per Container During `Runtime.Create`

**Files:**
- Modify: `pkg/runtime/runtime.go:75-270,315-380`
- Modify: `pkg/runtime/android_provider.go`
- Modify: `pkg/runtime/android_provider_test.go`
- Modify: `cmd/dokid/main.go:245-285`

**Interfaces:**
- Produces:

```go
func WithAndroidProviderRegistry(reg *AndroidProviderRegistry) RuntimeOption
func (rt *Runtime) selectContainerExecution(ctx context.Context, cfg *Config) (ExecutionMode, *ProviderSelection, error)
```

- Consumes: `AndroidProviderRegistry.SelectRequired`, provider-state persistence
- Invariant: no provider -> exact legacy `rt.mode`; selected provider -> `ModeAndroidNative`

- [ ] **Step 1: Write RED selection tests**

Test a runtime whose legacy mode is known from the host and an injected fake provider:

```go
func TestRuntimeSelectContainerExecutionUsesRequiredProvider(t *testing.T) {
    reg := NewAndroidProviderRegistry()
    _ = reg.Register(&fakeProvider{id:"fake", match: ProviderMatch{Matched:true, Required:true, Reason:"test"}})
    rt := NewRuntime(t.TempDir(), nil, WithAndroidProviderRegistry(reg))
    mode, sel, err := rt.selectContainerExecution(context.Background(), &Config{ImageRef:"example:1"})
    if err != nil { t.Fatal(err) }
    if mode != ModeAndroidNative || sel.Provider.ID() != "fake" { t.Fatalf("mode=%v sel=%+v", mode, sel) }
}
```

Also test:

- empty registry -> returns `rt.mode` and nil selection;
- non-Android explicit `cfg.Runtime` suppresses provider auto-selection and returns legacy mode (preserving current behavior in this phase);
- explicit `cfg.Runtime="android-native"` requires exactly one provider and errors if none;
- provider ambiguity propagates error and creates no container state.

- [ ] **Step 2: Verify RED**

Run:

```bash
go test ./pkg/runtime -run 'TestRuntimeSelectContainerExecution' -count=1
```

Expected: FAIL because runtime option/selection do not exist.

- [ ] **Step 3: Inject the registry**

Add:

```go
type Runtime struct {
    ...
    androidProviders *AndroidProviderRegistry
}

func WithAndroidProviderRegistry(reg *AndroidProviderRegistry) RuntimeOption {
    return func(rt *Runtime) { rt.androidProviders = reg }
}
```

In `cmd/dokid/main.go`:

```go
androidProviders := dr.NewAndroidProviderRegistry()
rt := dr.NewRuntime(execRoot, storeMgr,
    dr.WithRegistry(registry),
    dr.WithDNSAddr(dnsAddr),
    dr.WithVolumeResolver(volumeMgr),
    dr.WithAndroidProviderRegistry(androidProviders),
)
```

Do not register product providers in Phase 2.

- [ ] **Step 4: Implement create-time planning before rootfs extraction**

At the beginning of `Runtime.Create`, after ID/conflict validation:

```go
mode, selection, err := rt.selectContainerExecution(context.Background(), cfg)
if err != nil { return nil, err }
```

If `mode == ModeAndroidNative`:

1. create bundle/container directories;
2. do **not** call `fuse.CopyDir`, `extractLayers`, or `PrepareRootfs`;
3. leave `cfg.RootfsReady` empty;
4. persist `AndroidProviderState{Version:1, ProviderID:selection.Provider.ID(), MatchReason:selection.Match.Reason}`;
5. persist normal `ContainerState` with `Mode: ModeAndroidNative`.

For every other mode, preserve the existing rootfs path exactly and persist `Mode: mode`.

- [ ] **Step 5: Write RED/GREEN test proving Android-native skips OCI extraction**

Use a fake provider that requires the workload and an existing file containing invalid tar bytes as `ImageLayers`. `Runtime.Create` must succeed with `ModeAndroidNative`; the same config without provider selection must fail during extraction.

- [ ] **Step 6: Verify package regression**

Run:

```bash
go test ./pkg/runtime ./cmd/dokid -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add pkg/runtime/runtime.go pkg/runtime/android_provider.go pkg/runtime/android_provider_test.go cmd/dokid/main.go
git commit -m "feat(runtime): select Android providers per container"
```

---

### Task 5: Start Android-Native Workloads Through a Generic Process Bridge

**Files:**
- Create: `pkg/runtime/android_native.go`
- Create: `pkg/runtime/android_native_test.go`
- Modify: `pkg/runtime/runtime.go:860-1160`

**Interfaces:**
- Produces:

```go
func (rt *Runtime) resolvedDescriptor(cfg *Config) (WorkloadDescriptor, error)
func (rt *Runtime) startAndroidNative(state *ContainerState, logFile *os.File) (int, *exec.Cmd, error)
```

- Consumes: provider state, registry `Get`, `Ensure`, `Prepare`, volume resolver
- Invariant: executable path must be absolute; no provider => explicit error, no PRoot fallback

- [ ] **Step 1: Write RED tests for resolved mounts**

With logical:

```go
common.Mount{Type: common.MountVolume, Source:"db", Target:"/var/lib/data"}
```

and fake resolver `db -> /host/volumes/db/_data`, assert `resolvedDescriptor` contains:

```go
WorkloadMount{
    Mount: logical,
    HostPath: "/host/volumes/db/_data",
}
```

and the original `cfg.Mounts[0].Source` remains `db`.

Bind mounts use their declared absolute source as `HostPath`; tmpfs has empty `HostPath` in this phase.

- [ ] **Step 2: Write RED provider start tests**

Fake provider:

```go
Prepare(...) => &PreparedWorkload{
    Executable: "/bin/sh",
    Args: []string{"-c", "printf provider-started; sleep 2"},
    Env: []string{"PATH=/usr/bin:/bin"},
    Cwd: "/",
}
```

Create provider state + container state with `ModeAndroidNative`, call `Start`, then assert:

- state becomes `running`;
- PID > 0;
- `container.log` receives `provider-started`;
- fake provider `Ensure` and `Prepare` each called once.

Add failure tests:

- missing provider ID in registry -> error contains `Android provider "fake" is not registered`;
- relative executable `bin/tool` -> error contains `provider executable must be absolute`;
- `Ensure` error is returned unchanged/wrapped and PRoot is not attempted.

- [ ] **Step 3: Verify RED**

Run:

```bash
go test ./pkg/runtime -run 'TestResolvedDescriptor|TestAndroidNativeStart' -count=1
```

Expected: FAIL because bridge does not exist.

- [ ] **Step 4: Implement generic process start**

`startAndroidNative` flow:

```go
providerState, err := rt.loadAndroidProviderState(state.ID)
provider := rt.androidProviders.Get(providerState.ProviderID)
desc, err := rt.resolvedDescriptor(state.Config)
if err := provider.Ensure(context.Background(), desc); err != nil { ... }
prepared, err := provider.Prepare(context.Background(), desc)
if !filepath.IsAbs(prepared.Executable) { ... }
cmd := exec.Command(prepared.Executable, prepared.Args...)
cmd.Env = append([]string(nil), prepared.Env...)
cmd.Dir = prepared.Cwd
if cmd.Dir == "" { cmd.Dir = "/" }
cmd.Stdout = logFile
cmd.Stderr = logFile
cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid:true}
cmd.Start()
return cmd.Process.Pid, cmd, nil
```

Do not inherit the daemon environment unless the provider explicitly returns it.

- [ ] **Step 5: Make `state.Mode` authoritative in `Start`**

Use `state.Mode`, not `rt.mode`, for:

- namespace mount setup;
- `--init` path choice;
- process start dispatch.

Change process dispatch to:

```go
func (rt *Runtime) startProcess(mode ExecutionMode, state *ContainerState, cfg *Config, rootfsDir string, logFile *os.File) (int, *exec.Cmd, error)
```

with:

```go
case ModeAndroidNative:
    return rt.startAndroidNative(state, logFile)
```

All existing mode branches retain their current code.

- [ ] **Step 6: Verify GREEN and regressions**

Run:

```bash
go test ./pkg/runtime -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add pkg/runtime/android_native.go pkg/runtime/android_native_test.go pkg/runtime/runtime.go
git commit -m "feat(runtime): start Android-native provider workloads"
```

---

### Task 6: Route Exec and Healthchecks Through the Selected Provider

**Files:**
- Modify: `pkg/runtime/android_native.go`
- Modify: `pkg/runtime/android_native_test.go`
- Modify: `pkg/runtime/runtime.go:1520-1810`

**Interfaces:**
- Produces:

```go
func (rt *Runtime) execAndroidNative(state *ContainerState, cfg *ExecConfig) ([]byte, []byte, error)
func (rt *Runtime) execAttachAndroidNative(state *ContainerState, cfg *ExecConfig) (*ExecResult, error)
```

- Consumes: `AndroidWorkloadProvider.PrepareExec`
- Healthchecker automatically consumes this because existing healthchecks call `Runtime.Exec`

- [ ] **Step 1: Write RED exec tests**

Fake provider `PrepareExec` maps container args:

```go
[]string{"probe", "--ready"}
```

to:

```go
PreparedExec{Executable:"/bin/sh", Args:[]string{"-c", "printf ready"}, Env:[]string{"PATH=/usr/bin:/bin"}, Cwd:"/"}
```

Assert `Runtime.Exec` on a running `ModeAndroidNative` state returns stdout `ready` and provider receives the original `ExecConfig.Args`.

Test relative executable rejection and provider error propagation.

- [ ] **Step 2: Write streaming exec RED test**

For `ExecAttach`, use provider command `/bin/sh -c 'read line; printf "got:%s" "$line"'`; write `hello\n` to `res.Stdin`, close stdin, read stdout, call `Wait`, and expect `got:hello`.

- [ ] **Step 3: Verify RED**

Run:

```bash
go test ./pkg/runtime -run 'TestAndroidNativeExec|TestAndroidNativeExecAttach' -count=1
```

Expected: FAIL because exec bridge does not exist.

- [ ] **Step 4: Implement exec bridge**

Both buffered and attached exec paths:

1. load provider state;
2. get provider by persisted ID;
3. build resolved descriptor;
4. call `PrepareExec` with a copy of the Docker exec config;
5. validate absolute executable;
6. execute directly on Android host using only provider-returned env/cwd;
7. return actual process exit error/code to existing Docker exec machinery.

- [ ] **Step 5: Switch on `state.Mode` in `Runtime.Exec` and `ExecAttach`**

Replace `switch rt.mode` with `switch state.Mode` for these two methods. Add `ModeAndroidNative` branches before legacy paths.

Do not change behavior of existing modes.

- [ ] **Step 6: Add healthcheck integration test**

Create a running Android-native state whose `HealthCheck.Test` resolves through fake provider `PrepareExec`. Start a `HealthChecker` with a short interval and assert state transitions to `healthy` after provider returns exit 0.

- [ ] **Step 7: Verify GREEN**

Run:

```bash
go test ./pkg/runtime -run 'TestAndroidNativeExec|TestAndroidNativeExecAttach|TestAndroidNative.*Health' -count=1
go test ./pkg/runtime -count=1
```

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add pkg/runtime/android_native.go pkg/runtime/android_native_test.go pkg/runtime/runtime.go
git commit -m "feat(runtime): exec through Android workload providers"
```

---

### Task 7: Complete Generic Lifecycle and Provider Cleanup

**Files:**
- Modify: `pkg/runtime/runtime.go:1816-2052,2308-2320`
- Modify: `pkg/runtime/android_native_test.go`

**Interfaces:**
- Consumes: persisted provider ID + `AndroidWorkloadProvider.Cleanup`
- Produces: stop/kill/pause/unpause/delete behavior consistent after daemon state reload

- [ ] **Step 1: Write RED pause/unpause test after state reload**

Start a long-running Android-native fake workload, reload state using `Runtime.State` so `state.Cmd == nil`, call `Pause`, verify process status becomes stopped (`/proc/<pid>/status` contains `State:\tT` or signal behavior equivalent), call `Unpause`, verify it resumes.

The test should expose current behavior where pause only signals `state.Cmd.Process` and therefore does nothing after reload.

- [ ] **Step 2: Implement PID fallback for Pause/Unpause**

If cgroups are unavailable:

```go
process := state.Cmd.Process // when live
if process == nil && state.Pid > 0 {
    process, err = os.FindProcess(state.Pid)
}
if process == nil { return fmt.Errorf("container %s has no process", id) }
process.Signal(syscall.SIGSTOP) // or SIGCONT
```

Apply generically so legacy modes also improve without product logic.

- [ ] **Step 3: Write RED cleanup test**

Fake provider increments `cleanupCalls`. Create an exited `ModeAndroidNative` container and call `Runtime.Delete`. Assert cleanup is called once before container directory removal.

If `Cleanup` returns an error, `Delete` must return that error and retain container state for retry; it must not silently remove state.

- [ ] **Step 4: Implement provider cleanup boundary**

Before `cleanupContainer(state)` in `Delete`:

```go
if state.Mode == ModeAndroidNative {
    if err := rt.cleanupAndroidProvider(state); err != nil {
        rt.mu.Unlock()
        return err
    }
}
```

`cleanupAndroidProvider` loads provider state, gets the provider, builds a resolved descriptor while volumes still exist, and calls `provider.Cleanup`.

- [ ] **Step 5: Verify stop/kill/restart compatibility**

Add tests that generic `Stop` and `Kill` operate on Android-native PID and that an exited state retains `ModeAndroidNative` + provider state so `Start` re-enters the same provider instead of legacy mode.

- [ ] **Step 6: Run runtime regression**

Run:

```bash
go test ./pkg/runtime -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add pkg/runtime/runtime.go pkg/runtime/android_native_test.go
git commit -m "feat(runtime): complete Android provider lifecycle"
```

---

### Task 8: Add Fake-Provider Integration Coverage and Android Build Gate

**Files:**
- Create: `pkg/runtime/android_provider_integration_test.go`
- Modify: `docs/superpowers/plans/2026-08-21-android-native-provider-framework.md` only to append the verification record after execution

**Interfaces:**
- Consumes all Phase 2 framework interfaces
- Produces proof that provider selection survives disk state reload and legacy workloads are unaffected

- [ ] **Step 1: Write an integration test covering create → start → exec → restart → delete**

Use a fake provider that matches image `example/android-provider:1` and executes `/bin/sh` on the host. Sequence:

1. `Runtime.Create` with fake image metadata and one logical named volume.
2. Assert `state.Mode == ModeAndroidNative` and `android-provider.json` provider ID is `fake`.
3. `Runtime.Start`; assert running PID/log output.
4. `Runtime.Exec`; assert provider mapping output.
5. `Runtime.Stop`; assert exited.
6. Construct a **new Runtime instance** using the same root + same registry/resolver, simulating daemon restart.
7. `Start` the same container again; assert provider `Ensure/Prepare` are used and mode stays `android-native`.
8. `Delete`; assert provider cleanup and container directory removal.

- [ ] **Step 2: Add no-provider regression integration test**

Create a normal config with an empty provider registry and assert:

- `state.Mode == rt.Mode()`;
- rootfs extraction still occurs;
- no `android-provider.json` file exists.

- [ ] **Step 3: Run full repository tests**

Run:

```bash
go test ./... -count=1
git diff --check
```

Expected: all tests PASS and no whitespace errors.

- [ ] **Step 4: Build Android ARM64**

Run:

```bash
GOOS=android GOARCH=arm64 CGO_ENABLED=0 go build -o /root/doki-build/dokid-android-arm64-provider-framework ./cmd/dokid
sha256sum /root/doki-build/dokid-android-arm64-provider-framework
```

Expected: exit 0 and recorded SHA-256.

- [ ] **Step 5: On-device daemon smoke test**

Install the versioned binary under `~/doki-test/bin/`, update the `dokid` symlink, restart only from native Termux, then verify:

```bash
curl --unix-socket "$DOKI_SOCKET" http://localhost/version
```

and run the existing Phase 1 named-volume conformance script again to prove no regression:

```bash
scripts/android/test-compose-named-volumes.sh
```

Expected: version endpoint 200 and Phase 1 conformance PASS.

- [ ] **Step 6: Push and verify remote SHA**

```bash
git push origin feat/android-docker-compose
LOCAL=$(git rev-parse HEAD)
REMOTE=$(git ls-remote origin refs/heads/feat/android-docker-compose | awk '{print $1}')
test "$LOCAL" = "$REMOTE"
```

Expected: exact SHA match.

---

## Phase 2 Completion Gate

Do not implement the PostgreSQL provider until fresh evidence proves:

```text
[ ] existing ExecutionMode numeric values 0-11 are unchanged
[ ] android-native parses/stringifies and is persisted as mode 12
[ ] zero provider matches falls through to unchanged legacy runtime behavior
[ ] exactly one required provider selects android-native
[ ] ambiguous providers fail before container creation
[ ] provider decision persists in android-provider.json without env secrets
[ ] android-native create skips OCI rootfs extraction
[ ] provider executable must be absolute
[ ] provider Ensure/Prepare errors never fall back to PRoot
[ ] named volumes reach provider Prepare as resolved HostPath while persisted Source remains logical
[ ] Start records normal PID/status/log state
[ ] Exec and ExecAttach route through the persisted provider
[ ] healthchecks route through provider exec
[ ] Stop/Kill/Pause/Unpause work after state reload
[ ] restart reuses the same persisted provider decision
[ ] Delete calls provider Cleanup before state removal
[ ] fake-provider integration survives a new Runtime instance (daemon restart simulation)
[ ] no-provider integration still extracts/starts through legacy paths
[ ] go test ./... passes
[ ] Android ARM64 dokid build succeeds
[ ] Phase 1 Compose named-volume conformance still passes on-device
[ ] feature branch is pushed and local/remote SHA match
```

---

## Verification Record — 2026-08-21 Task 7/8

Fresh verification executed on MCP2 node `android-freecad` inside Debian/PRoot:

```text
TestAndroidNativeDeleteCallsProviderCleanup: PASS
TestAndroidNativeStopKillUsePersistedPID: PASS
TestAndroidNativeRestartUsesPersistedProviderID: PASS
TestAndroidProviderIntegrationLifecycleSurvivesRuntimeReload: PASS
TestAndroidProviderIntegrationNoProviderKeepsLegacyExtraction: PASS
/root/go1.27/bin/go test ./pkg/runtime -count=1: PASS
/root/go1.27/bin/go test ./... -count=1: PASS
git diff --check: PASS
```

Android ARM64 build gate:

```text
GOOS=android GOARCH=arm64 CGO_ENABLED=0 /root/go1.27/bin/go build -o /root/doki-build/dokid-android-arm64-provider-framework ./cmd/dokid: PASS
SHA256: 0e001be5b7a86b868c0ed41518b1ccb5392fcf1adb85d683ef997ff765666ee3
Size: 26211280 bytes
```

Native Termux daemon smoke verification after user restart:

```text
PID: 23695
/proc/23695/exe: /data/data/com.termux/files/home/doki-test/bin/dokid-android-arm64.20260821-provider-framework
Running SHA256: 0e001be5b7a86b868c0ed41518b1ccb5392fcf1adb85d683ef997ff765666ee3
/version: HTTP 200, OS=android, Arch=arm64, Go=go1.27.0
```

The first Phase 1 conformance attempt encountered a stale pre-existing `volconformance` container whose persisted state referenced a bundle/rootfs that no longer existed. The daemon log showed no new `POST /containers/create` for that failing container, proving Compose reused residual state rather than exercising fresh container creation. The cleanup trap removed that stale object. A second run from a clean `volconformance` state performed fresh container create/start cycles and passed:

```text
PASS: named volume lifecycle volconformance_probe_data
```

Push and local/remote SHA comparison are the only remaining Phase 2 gate steps.
