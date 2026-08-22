package main

import (
	"os"
	"path/filepath"
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
