package runtime

import (
	"fmt"

	"github.com/OpceanAI/Doki/pkg/common"
)

// VolumeResolver maps a logical Docker named-volume name to its physical host path.
type VolumeResolver interface {
	Resolve(name string) (string, error)
}

// WithVolumeResolver injects named-volume resolution into the runtime.
func WithVolumeResolver(resolver VolumeResolver) RuntimeOption {
	return func(rt *Runtime) {
		rt.volumeResolver = resolver
	}
}

// ResolveMount returns an execution-ready copy of a logical mount without
// mutating the persisted container configuration.
func (rt *Runtime) ResolveMount(m common.Mount) (common.Mount, error) {
	if m.Type != common.MountVolume {
		return m, nil
	}
	if rt.volumeResolver == nil {
		return common.Mount{}, fmt.Errorf("named volume %q: no volume resolver configured", m.Source)
	}
	path, err := rt.volumeResolver.Resolve(m.Source)
	if err != nil {
		return common.Mount{}, fmt.Errorf("resolve named volume %q: %w", m.Source, err)
	}
	resolved := m
	resolved.Type = common.MountBind
	resolved.Source = path
	return resolved, nil
}
