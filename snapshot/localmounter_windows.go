package snapshot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Microsoft/go-winio/pkg/bindfilter"
	"github.com/containerd/containerd/v2/core/mount"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/pkg/errors"
	"golang.org/x/sys/windows"
)

// crossOSLinuxMarker is the basename of the marker file util/winlayers writes
// into a snapshot whose Files/ tree holds Linux content wrapped as a Windows
// layer. When a parent carries it, Mount bypasses HCS and bind-mounts the
// parent's Files/ directly (HCS PrepareLayer can't expose Linux content
// without Hyper-V).
const crossOSLinuxMarker = ".cross-os-linux"

func (lm *localMounter) Mount() (string, error) {
	lm.mu.Lock()
	defer lm.mu.Unlock()

	if lm.target != "" {
		return lm.target, nil
	}

	if lm.mounts == nil && lm.mountable != nil {
		mounts, release, err := lm.mountable.Mount()
		if err != nil {
			return "", err
		}
		lm.mounts = mounts
		lm.release = release
	}

	// Windows can only mount a single mount at a given location.
	// Parent layers are carried in Options, opaquely to localMounter.
	if len(lm.mounts) != 1 {
		return "", errors.Wrapf(cerrdefs.ErrNotImplemented, "request to mount %d layers, only 1 is supported", len(lm.mounts))
	}

	m := lm.mounts[0]
	dir, err := os.MkdirTemp("", "buildkit-mount")
	if err != nil {
		return "", errors.Wrap(err, "failed to create temp dir")
	}

	if m.Type == "bind" || m.Type == "rbind" {
		if !m.ReadOnly() {
			// This is a rw bind mount, we can simply return the source.
			// NOTE(gabriel-samfira): This is safe to do if the source of the bind mount is a DOS path
			// of a local folder. If it's a \\?\Volume{} (for any reason that I can't think of now)
			// we should allow bindfilter.ApplyFileBinding() to mount it.
			return m.Source, nil
		}
		// The Windows snapshotter does not have any notion of bind mounts. We emulate
		// bind mounts here using the bind filter.
		if err := bindfilter.ApplyFileBinding(dir, m.Source, m.ReadOnly()); err != nil {
			return "", errors.Wrapf(err, "failed to mount %v", m)
		}
	} else if src, ok := crossOSLinuxSource(m); ok {
		// Cross-OS Linux source: bypass HCS (which can't expose Linux content
		// without Hyper-V) and bind-mount the parent's Files/ tree read-only.
		if err := bindfilter.ApplyFileBinding(dir, src, true); err != nil {
			return "", errors.Wrapf(err, "failed to bind-mount cross-OS layer files %s", src)
		}
	} else {
		// see https://github.com/moby/buildkit/issues/5807
		// if it's a race condition issue, do max 2 retries with some backoff
		// should adjust the retries if this persists but 1 retry
		// seems to be enough.
		if err := mountWithRetries(m, dir, 2); err != nil {
			return "", errors.Wrapf(err, "failed to mount %v", m)
		}
	}

	lm.target = dir
	return lm.target, nil
}

func mountWithRetries(m mount.Mount, dir string, retries int) error {
	errStr := "cannot access the file because it is being used by another process"
	backoff := 30 * time.Millisecond
	var err error

	for i := range retries + 1 {
		// i = 0 is first call and not a retry
		err = m.Mount(dir)
		if err == nil || i == retries {
			return err
		}
		if strings.Contains(err.Error(), errStr) {
			time.Sleep(time.Duration(i+1) * backoff)
		} else {
			return err
		}
	}

	return err
}

func (lm *localMounter) Unmount() error {
	lm.mu.Lock()
	defer lm.mu.Unlock()

	// NOTE(gabriel-samfira): Should we just return nil if len(lm.mounts) == 0?
	// Calling Mount() would fail on an instance of the localMounter where mounts contains
	// anything other than 1 mount.
	if len(lm.mounts) != 1 {
		return errors.Wrapf(cerrdefs.ErrNotImplemented, "request to mount %d layers, only 1 is supported", len(lm.mounts))
	}
	m := lm.mounts[0]

	if lm.target != "" {
		// A cross-OS Linux source is bind-filter mounted even though its mount
		// Type is "windows-layer", so re-derive that to pick the right teardown.
		// Checked last so the os.Stat is short-circuited for plain bind mounts.
		if m.Type == "bind" || m.Type == "rbind" || isCrossOSLinuxMount(m) {
			if err := bindfilter.RemoveFileBinding(lm.target); err != nil {
				// The following two errors denote that lm.target is not a mount point.
				if !errors.Is(err, windows.ERROR_INVALID_PARAMETER) && !errors.Is(err, windows.ERROR_NOT_FOUND) {
					return errors.Wrapf(err, "failed to unmount %v: %+v", lm.target, err)
				}
			}
		} else {
			// The containerd snapshotter uses the bind filter internally to mount windows-layer
			// volumes. We use same bind filter here to emulate bind mounts. In theory we could
			// simply call mount.Unmount() here, without the extra check for bind mounts and explicit
			// call to bindfilter.RemoveFileBinding() (above), but this would operate under the
			// assumption that the internal implementation in containerd will always be based on the
			// bind filter, which feels brittle.
			if err := mount.Unmount(lm.target, 0); err != nil {
				return errors.Wrapf(err, "failed to unmount %v: %+v", lm.target, err)
			}
		}
		os.RemoveAll(lm.target)
		lm.target = ""
	}

	if lm.release != nil {
		return lm.release()
	}

	return nil
}

// crossOSLinuxSource returns the parent's Files/ directory to bind-mount in
// lieu of HCS, when m is a windows-layer mount whose first parent carries the
// cross-OS marker.
func crossOSLinuxSource(m mount.Mount) (string, bool) {
	if m.Type != "windows-layer" {
		return "", false
	}
	parents := parseParentLayerPaths(m.Options)
	if len(parents) == 0 {
		return "", false
	}
	parent := parents[0]
	if _, err := os.Stat(filepath.Join(parent, crossOSLinuxMarker)); err != nil {
		return "", false
	}
	files := filepath.Join(parent, "Files")
	if _, err := os.Stat(files); err != nil {
		return "", false
	}
	return files, true
}

// isCrossOSLinuxMount reports whether the mount is a cross-OS Linux source
// layer (a windows-layer mount whose parent carries the cross-OS marker).
func isCrossOSLinuxMount(m mount.Mount) bool {
	_, ok := crossOSLinuxSource(m)
	return ok
}

// parseParentLayerPaths decodes the parentLayerPaths= option (a JSON list of
// absolute layer dirs, parent-first) the containerd snapshotter emits.
func parseParentLayerPaths(opts []string) []string {
	const prefix = "parentLayerPaths="
	for _, opt := range opts {
		if after, ok := strings.CutPrefix(opt, prefix); ok {
			var paths []string
			if err := json.Unmarshal([]byte(after), &paths); err == nil {
				return paths
			}
		}
	}
	return nil
}
