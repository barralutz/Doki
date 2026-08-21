package runtime

import (
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
	return cmd.Process.Pid, cmd, nil
}
