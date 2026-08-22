package main

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
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

type daemonActionKind int

const (
	daemonActionShutdown daemonActionKind = iota
	daemonActionReexec
)

type daemonAction struct {
	kind   daemonActionKind
	target string
	signal os.Signal
}

func waitForDaemonAction(signals <-chan os.Signal, resolve func() (string, error), onReexecError func(error)) daemonAction {
	for sig := range signals {
		if sig != syscall.SIGUSR2 {
			return daemonAction{kind: daemonActionShutdown, signal: sig}
		}
		target, err := resolve()
		if err != nil {
			if onReexecError != nil {
				onReexecError(err)
			}
			continue
		}
		return daemonAction{kind: daemonActionReexec, target: target, signal: sig}
	}
	return daemonAction{kind: daemonActionShutdown}
}

func execDaemon(target string, args, env []string, execFn func(string, []string, []string) error) error {
	if err := execFn(target, args, env); err != nil {
		return fmt.Errorf("exec dokid target %s: %w", target, err)
	}
	return nil
}
