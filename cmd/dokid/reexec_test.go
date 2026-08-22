package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

func writeExecutableFile(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("test-binary"), 0755); err != nil {
		t.Fatal(err)
	}
}

func TestResolveDaemonReexecTargetAcceptsVersionedSiblingSymlink(t *testing.T) {
	dir := t.TempDir()
	current := filepath.Join(dir, "dokid-v1")
	next := filepath.Join(dir, "dokid-v2")
	writeExecutableFile(t, current)
	writeExecutableFile(t, next)
	if err := os.Symlink(filepath.Base(next), filepath.Join(dir, "dokid")); err != nil {
		t.Fatal(err)
	}

	got, err := resolveDaemonReexecTarget(current)
	if err != nil {
		t.Fatal(err)
	}
	if got != next {
		t.Fatalf("target=%q want=%q", got, next)
	}
}

func TestResolveDaemonReexecTargetRejectsUnsafeCandidates(t *testing.T) {
	t.Run("candidate must be symlink", func(t *testing.T) {
		dir := t.TempDir()
		current := filepath.Join(dir, "dokid-v1")
		writeExecutableFile(t, current)
		writeExecutableFile(t, filepath.Join(dir, "dokid"))
		if _, err := resolveDaemonReexecTarget(current); err == nil {
			t.Fatal("regular file candidate unexpectedly accepted")
		}
	})

	t.Run("target stays in daemon directory", func(t *testing.T) {
		dir := t.TempDir()
		outside := t.TempDir()
		current := filepath.Join(dir, "dokid-v1")
		target := filepath.Join(outside, "dokid-v2")
		writeExecutableFile(t, current)
		writeExecutableFile(t, target)
		if err := os.Symlink(target, filepath.Join(dir, "dokid")); err != nil {
			t.Fatal(err)
		}
		if _, err := resolveDaemonReexecTarget(current); err == nil {
			t.Fatal("outside-directory target unexpectedly accepted")
		}
	})

	t.Run("target must be executable", func(t *testing.T) {
		dir := t.TempDir()
		current := filepath.Join(dir, "dokid-v1")
		target := filepath.Join(dir, "dokid-v2")
		writeExecutableFile(t, current)
		if err := os.WriteFile(target, []byte("not executable"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Base(target), filepath.Join(dir, "dokid")); err != nil {
			t.Fatal(err)
		}
		if _, err := resolveDaemonReexecTarget(current); err == nil {
			t.Fatal("non-executable target unexpectedly accepted")
		}
	})

	t.Run("target must differ from current executable", func(t *testing.T) {
		dir := t.TempDir()
		current := filepath.Join(dir, "dokid-v1")
		writeExecutableFile(t, current)
		if err := os.Symlink(filepath.Base(current), filepath.Join(dir, "dokid")); err != nil {
			t.Fatal(err)
		}
		if _, err := resolveDaemonReexecTarget(current); err == nil {
			t.Fatal("current executable unexpectedly accepted as reexec target")
		}
	})
}

func TestWaitForDaemonActionInvalidReexecContinuesToShutdownSignal(t *testing.T) {
	signals := make(chan os.Signal, 2)
	signals <- syscall.SIGUSR2
	signals <- syscall.SIGTERM

	var reported error
	action := waitForDaemonAction(
		signals,
		func() (string, error) { return "", errors.New("unsafe target") },
		func(err error) { reported = err },
	)

	if reported == nil || !strings.Contains(reported.Error(), "unsafe target") {
		t.Fatalf("reexec error not reported: %v", reported)
	}
	if action.kind != daemonActionShutdown {
		t.Fatalf("action kind=%v want shutdown", action.kind)
	}
	if action.signal != syscall.SIGTERM {
		t.Fatalf("action signal=%v want SIGTERM", action.signal)
	}
}

func TestWaitForDaemonActionValidReexecSelectsTarget(t *testing.T) {
	signals := make(chan os.Signal, 1)
	signals <- syscall.SIGUSR2

	action := waitForDaemonAction(
		signals,
		func() (string, error) { return "/tmp/dokid-v2", nil },
		nil,
	)

	if action.kind != daemonActionReexec {
		t.Fatalf("action kind=%v want reexec", action.kind)
	}
	if action.target != "/tmp/dokid-v2" {
		t.Fatalf("action target=%q want /tmp/dokid-v2", action.target)
	}
	if action.signal != syscall.SIGUSR2 {
		t.Fatalf("action signal=%v want SIGUSR2", action.signal)
	}
}

func TestExecDaemonPreservesTargetArgsAndEnvironment(t *testing.T) {
	target := "/data/data/com.termux/files/home/doki-test/bin/dokid-v2"
	args := []string{"./dokid", "--debug"}
	env := []string{"PREFIX=/data/data/com.termux/files/usr", "HOME=/data/data/com.termux/files/home"}
	sentinel := errors.New("exec returned")

	var gotTarget string
	var gotArgs, gotEnv []string
	err := execDaemon(target, args, env, func(path string, argv, environ []string) error {
		gotTarget = path
		gotArgs = append([]string(nil), argv...)
		gotEnv = append([]string(nil), environ...)
		return sentinel
	})

	if !errors.Is(err, sentinel) {
		t.Fatalf("exec error=%v want sentinel", err)
	}
	if gotTarget != target {
		t.Fatalf("target=%q want=%q", gotTarget, target)
	}
	if !reflect.DeepEqual(gotArgs, args) {
		t.Fatalf("args=%q want=%q", gotArgs, args)
	}
	if !reflect.DeepEqual(gotEnv, env) {
		t.Fatalf("env=%q want=%q", gotEnv, env)
	}
}
