package runtime

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

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

func (rt *Runtime) appendProotMountArgs(base []string, rootfs string, mounts []common.Mount) ([]string, error) {
	args := append([]string(nil), base...)
	cleanRootfs := filepath.Clean(rootfs)

	for _, logical := range mounts {
		mnt, err := rt.ResolveMount(logical)
		if err != nil {
			return nil, err
		}
		switch mnt.Type {
		case common.MountBind:
			if mnt.Source == "" || mnt.Target == "" {
				continue
			}
			targetInRootfs, err := common.SecureJoin(cleanRootfs, mnt.Target)
			if err != nil {
				return nil, fmt.Errorf("resolve mount target %s: %w", mnt.Target, err)
			}
			if err := os.MkdirAll(targetInRootfs, 0755); err != nil {
				return nil, fmt.Errorf("create mount target %s: %w", mnt.Target, err)
			}
			if mnt.ReadOnly {
				slog.Warn("proot cannot enforce read-only bind mount; mounting read-write", "target", mnt.Target)
			}
			args = append(args, "-b", mnt.Source+":"+mnt.Target)
		case common.MountTmpfs:
			target, err := common.SecureJoin(cleanRootfs, mnt.Target)
			if err != nil {
				return nil, fmt.Errorf("resolve tmpfs target %s: %w", mnt.Target, err)
			}
			if err := os.MkdirAll(target, 0755); err != nil {
				return nil, fmt.Errorf("create tmpfs target %s: %w", mnt.Target, err)
			}
			args = append(args, "-b", target+":"+mnt.Target)
		}
	}
	return args, nil
}

func (rt *Runtime) prepareNamedVolumes(rootfs string, mounts []common.Mount) error {
	for _, logical := range mounts {
		if logical.Type != common.MountVolume {
			continue
		}
		if logical.VolumeOptions != nil && logical.VolumeOptions.NoCopy {
			continue
		}

		resolved, err := rt.ResolveMount(logical)
		if err != nil {
			return err
		}
		entries, err := os.ReadDir(resolved.Source)
		if err != nil {
			return fmt.Errorf("read named volume %q: %w", logical.Source, err)
		}
		if len(entries) != 0 {
			continue
		}

		src, err := common.SecureJoin(filepath.Clean(rootfs), logical.Target)
		if err != nil {
			return fmt.Errorf("resolve volume seed target %s: %w", logical.Target, err)
		}
		info, err := os.Lstat(src)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect volume seed target %s: %w", logical.Target, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("volume seed target %s is not a directory", logical.Target)
		}
		if err := copyVolumeSeed(src, resolved.Source); err != nil {
			return fmt.Errorf("seed named volume %q: %w", logical.Source, err)
		}
	}
	return nil
}

func copyVolumeSeed(srcDir, dstDir string) error {
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		src := filepath.Join(srcDir, entry.Name())
		dst := filepath.Join(dstDir, entry.Name())
		info, err := os.Lstat(src)
		if err != nil {
			return err
		}

		switch {
		case info.Mode().IsDir():
			if err := os.Mkdir(dst, info.Mode().Perm()); err != nil {
				return err
			}
			if err := copyVolumeSeed(src, dst); err != nil {
				return err
			}
			if err := os.Chmod(dst, info.Mode().Perm()); err != nil {
				return err
			}
		case info.Mode().IsRegular():
			in, err := os.Open(src)
			if err != nil {
				return err
			}
			out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
			if err != nil {
				_ = in.Close()
				return err
			}
			_, copyErr := io.Copy(out, in)
			closeOutErr := out.Close()
			closeInErr := in.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeOutErr != nil {
				return closeOutErr
			}
			if closeInErr != nil {
				return closeInErr
			}
			if err := os.Chmod(dst, info.Mode().Perm()); err != nil {
				return err
			}
		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(src)
			if err != nil {
				return err
			}
			if err := os.Symlink(link, dst); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported special file %s with mode %s", src, info.Mode())
		}
	}
	return nil
}
