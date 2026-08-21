package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const androidProviderStateVersion = 1

type AndroidProviderState struct {
	Version     int    `json:"version"`
	ProviderID  string `json:"providerId"`
	MatchReason string `json:"matchReason,omitempty"`
}

func (rt *Runtime) androidProviderStatePath(containerID string) string {
	return filepath.Join(rt.root, "containers", containerID, "android-provider.json")
}

func (rt *Runtime) saveAndroidProviderState(containerID string, state AndroidProviderState) error {
	if strings.TrimSpace(state.ProviderID) == "" {
		return fmt.Errorf("Android provider state has empty provider ID")
	}
	if state.Version == 0 {
		state.Version = androidProviderStateVersion
	}
	if state.Version != androidProviderStateVersion {
		return fmt.Errorf("unsupported Android provider state version %d", state.Version)
	}
	dir := filepath.Join(rt.root, "containers", containerID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create Android provider state dir: %w", err)
	}
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal Android provider state: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "android-provider.json.tmp.*")
	if err != nil {
		return fmt.Errorf("create Android provider temp state: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write Android provider state: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("sync Android provider state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close Android provider state: %w", err)
	}
	if err := os.Rename(tmpName, rt.androidProviderStatePath(containerID)); err != nil {
		cleanup()
		return fmt.Errorf("commit Android provider state: %w", err)
	}
	return nil
}

func (rt *Runtime) loadAndroidProviderState(containerID string) (*AndroidProviderState, error) {
	path := rt.androidProviderStatePath(containerID)
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("load Android provider state: %w", err)
	}
	var state AndroidProviderState
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, fmt.Errorf("parse Android provider state: %w", err)
	}
	if state.Version != androidProviderStateVersion {
		return nil, fmt.Errorf("unsupported Android provider state version %d", state.Version)
	}
	if strings.TrimSpace(state.ProviderID) == "" {
		return nil, fmt.Errorf("Android provider state has empty provider ID")
	}
	return &state, nil
}
