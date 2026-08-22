package main

import (
	"fmt"
	"os"
	"path/filepath"
)

func resolveDaemonReexecTarget(currentExecutable string) (string, error) {
	current, err := filepath.Abs(currentExecutable)
	if err != nil {
		return "", fmt.Errorf("resolve current dokid executable: %w", err)
	}
	currentDir, err := filepath.EvalSymlinks(filepath.Dir(current))
	if err != nil {
		return "", fmt.Errorf("resolve current dokid directory: %w", err)
	}

	link := filepath.Join(currentDir, "dokid")
	linkInfo, err := os.Lstat(link)
	if err != nil {
		return "", fmt.Errorf("inspect dokid reexec symlink %s: %w", link, err)
	}
	if linkInfo.Mode()&os.ModeSymlink == 0 {
		return "", fmt.Errorf("dokid reexec candidate is not a symlink: %s", link)
	}

	resolved, err := filepath.EvalSymlinks(link)
	if err != nil {
		return "", fmt.Errorf("resolve dokid reexec symlink %s: %w", link, err)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", fmt.Errorf("resolve dokid reexec target: %w", err)
	}
	if filepath.Dir(resolved) != currentDir {
		return "", fmt.Errorf("dokid reexec target escapes daemon directory: %s", resolved)
	}

	targetInfo, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("inspect dokid reexec target %s: %w", resolved, err)
	}
	if !targetInfo.Mode().IsRegular() {
		return "", fmt.Errorf("dokid reexec target is not a regular file: %s", resolved)
	}
	if targetInfo.Mode().Perm()&0111 == 0 {
		return "", fmt.Errorf("dokid reexec target is not executable: %s", resolved)
	}

	currentInfo, err := os.Stat(current)
	if err != nil {
		return "", fmt.Errorf("inspect current dokid executable %s: %w", current, err)
	}
	if os.SameFile(currentInfo, targetInfo) {
		return "", fmt.Errorf("dokid reexec target is already running: %s", resolved)
	}
	return resolved, nil
}
