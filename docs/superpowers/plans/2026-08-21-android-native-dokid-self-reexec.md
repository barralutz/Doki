# Android Native `dokid` Self-Reexec Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a safe `SIGUSR2` self-reexec path so MCP2 can activate future Android-native `dokid` upgrades without spawning `dokid` from PRoot.

**Architecture:** The current native daemon receives `SIGUSR2`, validates only the sibling `dokid` symlink, gracefully closes HTTP/CRI, then replaces itself with the validated target through `syscall.Exec`. Invalid targets do not stop the daemon.

**Tech Stack:** Go 1.27, `os/signal`, `syscall.Exec`, Unix symlinks, existing Doki HTTP/CRI shutdown flow.

**Spec:** `docs/superpowers/specs/2026-08-21-android-native-dokid-self-reexec-design.md`

## Global Constraints

- Work directly on `feat/android-docker-compose`; do not create a worktree or change branches.
- Do not use agents/subagents.
- Do not start/restart native `dokid` from MCP2/PRoot.
- Use TDD: RED -> minimal implementation -> GREEN.
- Only the sibling symlink named `dokid` may select a reexec target.
- Invalid `SIGUSR2` requests must leave the current daemon running.

---

### Task 1: Safe Reexec Target Resolution

**Files:**
- Create: `cmd/dokid/reexec.go`
- Test: `cmd/dokid/reexec_test.go`

**Interfaces:**
- Produces: `resolveDaemonReexecTarget(currentExecutable string) (string, error)`

- [ ] Write a failing test proving a sibling `dokid` symlink resolves to a different executable sibling.
- [ ] Run the focused test and observe RED.
- [ ] Implement minimal resolution.
- [ ] Run focused test and observe GREEN.
- [ ] Add a failing test for non-symlink, outside-directory, non-executable, and current-binary targets.
- [ ] Implement the safety checks and return descriptive errors.
- [ ] Run `go test ./cmd/dokid -count=1` and commit.

### Task 2: Signal Selection Without Unsafe Shutdown

**Files:**
- Modify: `cmd/dokid/reexec.go`
- Modify: `cmd/dokid/reexec_test.go`

**Interfaces:**
- Produces: `daemonAction`, `waitForDaemonAction(signals <-chan os.Signal, resolve func() (string, error), onReexecError func(error)) daemonAction`

- [ ] Write a failing test proving `SIGUSR2` with an invalid target reports the error and continues until a normal shutdown signal arrives.
- [ ] Run focused test and observe RED.
- [ ] Implement the signal loop.
- [ ] Add a failing test proving valid `SIGUSR2` selects reexec with the resolved target.
- [ ] Implement the minimal valid-target branch.
- [ ] Run `go test ./cmd/dokid -count=1` and commit.

### Task 3: Preserve Process Contract Across Exec

**Files:**
- Modify: `cmd/dokid/reexec.go`
- Modify: `cmd/dokid/reexec_test.go`

**Interfaces:**
- Produces: `execDaemon(target string, args, env []string, execFn func(string, []string, []string) error) error`

- [ ] Write a failing test capturing the target, argv, and environment passed to an injected exec function.
- [ ] Run focused test and observe RED.
- [ ] Implement the minimal wrapper.
- [ ] Run focused and package tests and commit.

### Task 4: Integrate Graceful Reexec Into `dokid`

**Files:**
- Modify: `cmd/dokid/main.go`
- Test: `cmd/dokid/reexec_test.go`

**Interfaces:**
- Consumes: Task 1-3 helpers.
- Produces: `SIGUSR2 -> validate -> graceful shutdown -> syscall.Exec` in the daemon main lifecycle.

- [ ] Register `SIGUSR2` along with existing shutdown signals.
- [ ] Replace the one-shot `api.WaitForSignal(rootCancel)` bridge with `waitForDaemonAction`.
- [ ] Keep current HTTP/CRI graceful shutdown order.
- [ ] After shutdown, invoke `execDaemon(action.target, os.Args, os.Environ(), syscall.Exec)` only for a reexec action.
- [ ] Run `go test ./cmd/dokid -count=1`, then `go test ./... -count=1`, then `git diff --check`.
- [ ] Commit.

### Task 5: Android Build and One-Time Native Activation Gate

**Files:**
- No repository source changes required unless verification finds a defect.

**Interfaces:**
- Produces: first self-reexec-capable Android ARM64 `dokid` binary.

- [ ] Build with `GOOS=android GOARCH=arm64 CGO_ENABLED=0`.
- [ ] Install a versioned binary in `~/doki-test/bin` and atomically update the sibling `dokid` symlink without touching the running process.
- [ ] Record SHA256 and verify the old daemon is still running.
- [ ] Ask the user for the one final manual Termux restart.

### Task 6: Prove MCP2-Driven Native Reexec

**Files:**
- Modify documentation only if observed behavior needs clarification.

**Interfaces:**
- Acceptance: unchanged PID, changed `/proc/<pid>/exe` and SHA, recovered Unix socket/API after MCP2 sends `SIGUSR2`.

- [ ] After the user starts the first self-reexec-capable binary, verify PID/executable/SHA.
- [ ] Create/install a second versioned build with a distinct SHA (build metadata is sufficient) and repoint `dokid`.
- [ ] From MCP2, send `SIGUSR2` to the native PID.
- [ ] Verify the PID is unchanged and executable/SHA changed to the second build.
- [ ] Verify `/version` and the Docker API socket are healthy.
- [ ] Re-run the PostgreSQL Phase 3 conformance from the exact point previously blocked.
