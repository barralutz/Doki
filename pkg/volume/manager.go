package volume

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/OpceanAI/Doki/pkg/common"
)

// Manager owns Docker-compatible named-volume metadata and backing data.
type Manager struct {
	mu      sync.RWMutex
	root    string
	volumes map[string]*common.VolumeInfo
}

// NewManager creates a named-volume manager rooted at root.
func NewManager(root string) (*Manager, error) {
	if err := common.EnsureDir(root); err != nil {
		return nil, fmt.Errorf("create volume root: %w", err)
	}
	m := &Manager{
		root:    root,
		volumes: make(map[string]*common.VolumeInfo),
	}
	if err := m.loadFromDisk(); err != nil {
		return nil, err
	}
	return m, nil
}

func validName(name string) bool {
	if name == "" || strings.Contains(name, "/") || strings.Contains(name, "..") || strings.Contains(name, string(os.PathSeparator)) {
		return false
	}
	return true
}

func (m *Manager) volumeDir(name string) string {
	return filepath.Join(m.root, name)
}

func (m *Manager) dataDir(name string) string {
	return filepath.Join(m.volumeDir(name), "_data")
}

func (m *Manager) metadataPath(name string) string {
	return filepath.Join(m.volumeDir(name), "volume.json")
}

func (m *Manager) loadFromDisk() error {
	entries, err := os.ReadDir(m.root)
	if err != nil {
		return fmt.Errorf("read volume root: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		volPath := m.metadataPath(entry.Name())
		data, err := os.ReadFile(volPath)
		if err != nil {
			slog.Warn("skip unreadable volume metadata", "path", volPath, "err", err)
			continue
		}
		var vol common.VolumeInfo
		if err := json.Unmarshal(data, &vol); err != nil {
			slog.Warn("skip invalid volume metadata", "path", volPath, "err", err)
			continue
		}
		if !validName(vol.Name) {
			slog.Warn("skip invalid volume name", "path", volPath, "name", vol.Name)
			continue
		}

		volDir := filepath.Clean(m.volumeDir(vol.Name))
		dataDir := filepath.Clean(m.dataDir(vol.Name))
		mountpoint := filepath.Clean(vol.Mountpoint)
		switch mountpoint {
		case ".", "", volDir:
			if err := m.migrateLegacyVolume(vol.Name, &vol); err != nil {
				return err
			}
		case dataDir:
			if err := common.EnsureDir(dataDir); err != nil {
				return fmt.Errorf("ensure volume data directory %s: %w", dataDir, err)
			}
		default:
			return fmt.Errorf("invalid volume metadata %q: mountpoint %q is outside managed data directory %q", vol.Name, vol.Mountpoint, dataDir)
		}
		m.volumes[vol.Name] = &vol
	}
	return nil
}

func (m *Manager) migrateLegacyVolume(name string, info *common.VolumeInfo) error {
	volDir := m.volumeDir(name)
	dataDir := m.dataDir(name)
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return fmt.Errorf("create migrated volume data directory %s: %w", dataDir, err)
	}

	entries, err := os.ReadDir(volDir)
	if err != nil {
		return fmt.Errorf("read legacy volume %s: %w", name, err)
	}
	for _, entry := range entries {
		switch entry.Name() {
		case "volume.json", "volume.json.tmp", "_data":
			continue
		}
		src := filepath.Join(volDir, entry.Name())
		dst := filepath.Join(dataDir, entry.Name())
		if _, err := os.Lstat(dst); err == nil {
			return fmt.Errorf("ambiguous legacy volume migration %q: both %s and %s exist", name, src, dst)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect migrated volume destination %s: %w", dst, err)
		}
		if err := os.Rename(src, dst); err != nil {
			return fmt.Errorf("migrate legacy volume entry %s to %s: %w", src, dst, err)
		}
	}

	info.Mountpoint = dataDir
	if err := writeMetadataAtomic(m.metadataPath(name), info); err != nil {
		return fmt.Errorf("persist migrated volume %s: %w", name, err)
	}
	return nil
}

func writeMetadataAtomic(path string, vol *common.VolumeInfo) error {
	data, err := json.Marshal(vol)
	if err != nil {
		return fmt.Errorf("marshal volume metadata: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return fmt.Errorf("write volume metadata: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("commit volume metadata: %w", err)
	}
	return nil
}

// Create creates a named volume with Docker-like metadata and _data separation.
func (m *Manager) Create(name string, driver string, opts map[string]string, labels map[string]string) (*common.VolumeInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !validName(name) {
		return nil, fmt.Errorf("invalid volume name: %q contains path traversal characters", name)
	}
	if _, exists := m.volumes[name]; exists {
		return nil, common.NewErrConflict("volume", name)
	}

	volDir := m.volumeDir(name)
	mountpoint := m.dataDir(name)
	if err := common.EnsureDir(mountpoint); err != nil {
		return nil, fmt.Errorf("create volume data directory: %w", err)
	}

	if driver == "" {
		driver = "local"
	}
	vol := &common.VolumeInfo{
		Name:       name,
		Driver:     driver,
		Mountpoint: mountpoint,
		Labels:     labels,
		Scope:      "local",
		Options:    opts,
		CreatedAt:  time.Now(),
	}
	if err := writeMetadataAtomic(filepath.Join(volDir, "volume.json"), vol); err != nil {
		return nil, err
	}
	m.volumes[name] = vol
	return vol, nil
}

// Resolve returns the physical data directory for a logical named volume.
func (m *Manager) Resolve(name string) (string, error) {
	v, err := m.Get(name)
	if err != nil {
		return "", err
	}
	return v.Mountpoint, nil
}

func (m *Manager) Get(name string) (*common.VolumeInfo, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	vol, ok := m.volumes[name]
	if !ok {
		return nil, common.NewErrNotFound("volume", name)
	}
	return vol, nil
}

func (m *Manager) List() []*common.VolumeInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	vols := make([]*common.VolumeInfo, 0, len(m.volumes))
	for _, v := range m.volumes {
		vols = append(vols, v)
	}
	return vols
}

func (m *Manager) Remove(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.volumes[name]; !ok {
		return common.NewErrNotFound("volume", name)
	}
	if err := os.RemoveAll(m.volumeDir(name)); err != nil {
		return fmt.Errorf("remove volume data: %w", err)
	}
	delete(m.volumes, name)
	return nil
}

func (m *Manager) Prune(referencedVolumes map[string]bool) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var pruned []string
	for name := range m.volumes {
		if referencedVolumes[name] {
			continue
		}
		if err := os.RemoveAll(m.volumeDir(name)); err != nil {
			return pruned, fmt.Errorf("remove volume %s: %w", name, err)
		}
		delete(m.volumes, name)
		pruned = append(pruned, name)
	}
	return pruned, nil
}
