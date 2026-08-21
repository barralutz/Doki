package api

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/OpceanAI/Doki/pkg/common"
	"github.com/OpceanAI/Doki/pkg/volume"
)

func parseHostConfigBind(spec string) (common.Mount, error) {
	parts := strings.SplitN(spec, ":", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return common.Mount{}, fmt.Errorf("invalid bind specification %q: expected source:target[:ro|rw]", spec)
	}

	source := parts[0]
	target := parts[1]
	if !filepath.IsAbs(target) || filepath.Clean(target) != target {
		return common.Mount{}, fmt.Errorf("invalid mount target %q: must be an absolute clean path", target)
	}

	readOnly := false
	if len(parts) == 3 && parts[2] != "" {
		switch parts[2] {
		case "ro":
			readOnly = true
		case "rw":
		default:
			return common.Mount{}, fmt.Errorf("unsupported bind option %q in %q", parts[2], spec)
		}
	}

	if filepath.IsAbs(source) {
		if filepath.Clean(source) != source {
			return common.Mount{}, fmt.Errorf("invalid bind mount source %q: must be a clean absolute path", source)
		}
		if isSensitiveBindSource(source) {
			return common.Mount{}, fmt.Errorf("bind mount source not allowed: %s", source)
		}
		return common.Mount{Type: common.MountBind, Source: source, Target: target, ReadOnly: readOnly}, nil
	}

	if !volume.ValidName(source) {
		return common.Mount{}, fmt.Errorf("invalid named volume source %q", source)
	}
	return common.Mount{Type: common.MountVolume, Source: source, Target: target, ReadOnly: readOnly}, nil
}

func (s *Server) ensureNamedVolume(name string) error {
	if s.volumes == nil {
		return fmt.Errorf("named volume %q: volume manager is not configured", name)
	}
	if _, err := s.volumes.Get(name); err == nil {
		return nil
	} else if !common.IsNotFound(err) {
		return err
	}

	if _, err := s.volumes.Create(name, "local", nil, nil); err != nil {
		if common.IsConflict(err) {
			_, getErr := s.volumes.Get(name)
			return getErr
		}
		return err
	}
	return nil
}
