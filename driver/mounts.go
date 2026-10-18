package steamcmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hashicorp/nomad/plugins/drivers"
)

// setupMounts makes Nomad volume_mount blocks (CSI and host volumes) visible
// inside the task directory.
//
// This driver runs with FSIsolationNone, so Nomad does not bind-mount
// anything for us: it only hands the driver a list of {HostPath, TaskPath}
// pairs in cfg.Mounts, where HostPath is where the volume is staged on the
// client and TaskPath is the volume_mount `destination`. Without this step a
// volume_mount is silently accepted but the destination never exists.
//
// Each TaskPath is resolved relative to taskDir (a leading "/" is ignored, so
// destination "/data" and "data" are equivalent) and created as a symlink to
// HostPath. Paths that would escape taskDir are rejected. Read-only mounts
// cannot be enforced through a symlink; the returned warnings list them.
func setupMounts(taskDir string, mounts []*drivers.MountConfig) (warnings []string, err error) {
	for _, m := range mounts {
		if m == nil || m.HostPath == "" || m.TaskPath == "" {
			continue
		}
		target, terr := mountTarget(taskDir, m.TaskPath)
		if terr != nil {
			return warnings, terr
		}
		if err := linkMount(m.HostPath, target); err != nil {
			return warnings, fmt.Errorf("volume_mount %q: %w", m.TaskPath, err)
		}
		if m.Readonly {
			warnings = append(warnings, fmt.Sprintf("volume_mount %q is read_only but this driver cannot enforce that (no filesystem isolation)", m.TaskPath))
		}
	}
	return warnings, nil
}

// mountTarget resolves a volume_mount destination to a path inside taskDir.
func mountTarget(taskDir, taskPath string) (string, error) {
	rel := strings.TrimLeft(filepath.ToSlash(taskPath), "/")
	target := filepath.Join(taskDir, filepath.FromSlash(rel))
	back, err := filepath.Rel(taskDir, target)
	if err != nil || back == "." || back == ".." || strings.HasPrefix(back, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("volume_mount destination %q must be a path inside the task directory", taskPath)
	}
	return target, nil
}

// linkMount creates target as a symlink to hostPath, replacing a stale
// symlink or an empty directory, and refusing to clobber real content.
func linkMount(hostPath, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	if st, err := os.Lstat(target); err == nil {
		switch {
		case st.Mode()&os.ModeSymlink != 0:
			if cur, rerr := os.Readlink(target); rerr == nil && cur == hostPath {
				return nil
			}
			if err := os.Remove(target); err != nil {
				return err
			}
		case st.IsDir():
			if err := os.Remove(target); err != nil { // only succeeds when empty
				return fmt.Errorf("%s exists and is not empty", target)
			}
		default:
			return fmt.Errorf("%s already exists and is not a directory", target)
		}
	}
	return os.Symlink(hostPath, target)
}
