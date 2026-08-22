# Android Native `dokid` Self-Reexec Design

## Goal

Allow MCP2 operating from Debian/PRoot to deploy and activate a new Android-native `dokid` binary without asking the user to manually kill/start the daemon in Termux after the first self-reexec-capable release is installed.

## Constraints

- `dokid` must remain an Android-native Termux process; MCP2/PRoot must never spawn the replacement daemon.
- No generic native command bridge is introduced.
- Existing `SIGINT`, `SIGTERM`, and `SIGHUP` shutdown behavior remains graceful.
- `SIGUSR2` is reserved for a controlled daemon self-reexec.
- The first release containing this feature still requires one manual native restart. Later upgrades must be triggerable from MCP2 with a signal.

## Reexec Target Contract

The running daemon resolves its own executable path and examines exactly one candidate: a sibling named `dokid` in the same directory.

The candidate must:

1. exist as a symbolic link;
2. resolve to a regular executable file;
3. resolve to a file in the same canonical directory as the currently running executable;
4. resolve to a different file from the currently running executable.

A broken link, non-link, directory escape, non-executable target, or no-op link back to the current binary is rejected. Rejection does not shut the daemon down; it logs the reason and continues listening for signals.

## Signal and Shutdown Flow

`SIGINT`, `SIGTERM`, or `SIGHUP` selects normal shutdown. `SIGUSR2` resolves and validates the reexec target. A valid target selects reexec; an invalid target is ignored after logging.

For a valid reexec request, `dokid` performs the same graceful HTTP and CRI shutdown used today. Only after those resources are closed does it call `syscall.Exec(target, os.Args, os.Environ())`.

Because `execve(2)` is issued by the already-native Termux process, the replacement keeps the native Android process context and PID rather than becoming a child of PRoot. The new daemon recreates the Unix socket using the existing startup behavior.

## Verification

Automated tests cover safe target resolution, unsafe target rejection, signal behavior, and argument/environment preservation. Repository-wide tests and an Android ARM64 build must pass before installation.

The on-device acceptance test is:

1. manually start the first self-reexec-capable binary natively once;
2. install a second versioned test build and move the sibling `dokid` symlink to it;
3. send `SIGUSR2` from MCP2/PRoot;
4. verify the PID is unchanged while `/proc/<pid>/exe` and SHA256 change to the new target;
5. verify the Docker API socket and `/version` recover successfully.
