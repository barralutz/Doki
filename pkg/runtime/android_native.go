package runtime

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/OpceanAI/Doki/pkg/common"
)

func (rt *Runtime) resolvedDescriptor(cfg *Config) (WorkloadDescriptor, error) {
	if cfg == nil {
		return WorkloadDescriptor{}, fmt.Errorf("Android provider workload has nil config")
	}
	mounts := make([]WorkloadMount, 0, len(cfg.Mounts))
	for _, logical := range cfg.Mounts {
		wm := WorkloadMount{Mount: logical}
		switch logical.Type {
		case common.MountVolume:
			resolved, err := rt.ResolveMount(logical)
			if err != nil {
				return WorkloadDescriptor{}, err
			}
			wm.HostPath = resolved.Source
		case common.MountBind:
			wm.HostPath = logical.Source
		}
		mounts = append(mounts, wm)
	}
	return descriptorFromConfig(cfg, mounts), nil
}

func (rt *Runtime) androidProviderForState(state *ContainerState) (AndroidWorkloadProvider, error) {
	if rt.androidProviders == nil {
		return nil, fmt.Errorf("Android provider registry is not configured")
	}
	providerState, err := rt.loadAndroidProviderState(state.ID)
	if err != nil {
		return nil, err
	}
	provider := rt.androidProviders.Get(providerState.ProviderID)
	if provider == nil {
		return nil, fmt.Errorf("Android provider %q is not registered", providerState.ProviderID)
	}
	return provider, nil
}

func (rt *Runtime) cleanupAndroidProvider(state *ContainerState) error {
	provider, err := rt.androidProviderForState(state)
	if err != nil {
		return err
	}
	desc, err := rt.resolvedDescriptor(state.Config)
	if err != nil {
		return err
	}
	if err := provider.Cleanup(context.Background(), desc); err != nil {
		return fmt.Errorf("Android provider %q cleanup: %w", provider.ID(), err)
	}
	return nil
}

func (rt *Runtime) startAndroidNative(state *ContainerState, logFile *os.File) (int, *exec.Cmd, error) {
	provider, err := rt.androidProviderForState(state)
	if err != nil {
		return 0, nil, err
	}
	desc, err := rt.resolvedDescriptor(state.Config)
	if err != nil {
		return 0, nil, err
	}
	if err := provider.Ensure(context.Background(), desc); err != nil {
		return 0, nil, fmt.Errorf("Android provider %q ensure: %w", provider.ID(), err)
	}
	prepared, err := provider.Prepare(context.Background(), desc)
	if err != nil {
		return 0, nil, fmt.Errorf("Android provider %q prepare: %w", provider.ID(), err)
	}
	if prepared == nil {
		return 0, nil, fmt.Errorf("Android provider %q returned nil prepared workload", provider.ID())
	}
	if !filepath.IsAbs(prepared.Executable) {
		return 0, nil, fmt.Errorf("provider executable must be absolute: %q", prepared.Executable)
	}

	cmd := exec.Command(prepared.Executable, prepared.Args...)
	cmd.Env = append([]string(nil), prepared.Env...)
	cmd.Dir = prepared.Cwd
	if cmd.Dir == "" {
		cmd.Dir = "/"
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	broker, err := rt.setupStdio(cmd, state.Config, logFile)
	if err != nil {
		return 0, nil, err
	}
	if broker == nil {
		cmd.Stdout = logFile
		cmd.Stderr = logFile
	}
	if err := cmd.Start(); err != nil {
		return 0, nil, fmt.Errorf("start Android provider process: %w", err)
	}
	if broker != nil {
		broker.afterStart()
		rt.registerBroker(state.ID, broker)
	}
	if err := rt.replacePortProxies(state.ID, prepared.PortForwards); err != nil {
		if broker != nil {
			broker.Close()
		}
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_, _ = cmd.Process.Wait()
		rt.closePortProxies(state.ID)
		return 0, nil, fmt.Errorf("start Android provider TCP forward: %w", err)
	}
	return cmd.Process.Pid, cmd, nil
}

func (rt *Runtime) prepareAndroidNativeExec(state *ContainerState, cfg *ExecConfig) (*exec.Cmd, error) {
	provider, err := rt.androidProviderForState(state)
	if err != nil {
		return nil, err
	}
	desc, err := rt.resolvedDescriptor(state.Config)
	if err != nil {
		return nil, err
	}
	prepared, err := provider.PrepareExec(context.Background(), desc, cfg)
	if err != nil {
		return nil, fmt.Errorf("Android provider %q prepare exec: %w", provider.ID(), err)
	}
	if prepared == nil {
		return nil, fmt.Errorf("Android provider %q returned nil prepared exec", provider.ID())
	}
	if !filepath.IsAbs(prepared.Executable) {
		return nil, fmt.Errorf("provider executable must be absolute: %q", prepared.Executable)
	}
	cmd := exec.Command(prepared.Executable, prepared.Args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Env = append([]string(nil), prepared.Env...)
	cmd.Dir = prepared.Cwd
	if cmd.Dir == "" {
		cmd.Dir = "/"
	}
	return cmd, nil
}

func (rt *Runtime) execAndroidNative(state *ContainerState, cfg *ExecConfig) ([]byte, []byte, error) {
	cmd, err := rt.prepareAndroidNativeExec(state, cfg)
	if err != nil {
		return nil, nil, err
	}
	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf
	err = cmd.Run()
	return stdoutBuf.Bytes(), stderrBuf.Bytes(), err
}

func (rt *Runtime) execAttachAndroidNative(state *ContainerState, cfg *ExecConfig) (*ExecResult, error) {
	cmd, err := rt.prepareAndroidNativeExec(state, cfg)
	if err != nil {
		return nil, err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("Android provider exec stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("Android provider exec stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("Android provider exec stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start Android provider exec: %w", err)
	}
	return &ExecResult{
		Stdin:  stdin,
		Stdout: stdout,
		Stderr: stderr,
		Wait:   cmd.Wait,
		Pid:    cmd.Process.Pid,
	}, nil
}
