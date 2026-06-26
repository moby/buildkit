//go:build windows

package winlayers

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Microsoft/go-winio"
	"github.com/Microsoft/hcsshim"
	"github.com/Microsoft/hcsshim/pkg/ociwclayer"
	"github.com/containerd/containerd/v2/core/mount"
	"github.com/moby/buildkit/util/bklog"
	"github.com/pkg/errors"
)

// importLinuxLayerAsWindows imports a Linux tar (already wrapped into Windows
// layer format) via HCS so the Windows snapshotter can mount it.
func importLinuxLayerAsWindows(ctx context.Context, snapshotDir string, wrappedTar io.Reader, parentPaths []string) (int64, error) {
	// The snapshotter's Prepare may pre-create Files/ or Hives/, which
	// ImportLayerFromTar (FILE_CREATE) rejects; remove them first.
	for _, name := range []string{"Files", "Hives"} {
		if err := os.RemoveAll(filepath.Join(snapshotDir, name)); err != nil {
			return 0, errors.Wrapf(err, "failed to clean %s before import", name)
		}
	}

	// ProcessBaseLayer checks the process token, not the thread impersonation
	// token RunWithPrivileges sets, so enable privileges process-wide.
	requiredPrivileges := []string{winio.SeBackupPrivilege, winio.SeRestorePrivilege, winio.SeSecurityPrivilege}
	if err := winio.EnableProcessPrivileges(requiredPrivileges); err != nil {
		return 0, errors.Wrap(err, "failed to enable required privileges")
	}
	defer winio.DisableProcessPrivileges(requiredPrivileges)

	size, err := ociwclayer.ImportLayerFromTar(ctx, wrappedTar, snapshotDir, parentPaths)
	if err != nil {
		// ImportLayerFromTar wrote Files/ but its ProcessBaseLayer step fails on
		// Linux content with a host-dependent message, so match the
		// "ProcessBaseLayer" marker rather than a specific string.
		if isProcessBaseLayerError(err) && hasFilesDir(snapshotDir) {
			bklog.G(ctx).Debugf("ProcessBaseLayer failed (expected for Linux content): %v; converting Files/ to a base layer", err)
			// ConvertToBaseLayer finalizes a clean Files/-only dir into a base
			// layer (generates hives, VHDs and Layout), so drop the partial
			// artifacts the failed import left behind first.
			if err := removeAllExceptFiles(ctx, snapshotDir); err != nil {
				return 0, errors.Wrap(err, "failed to clean snapshot before base-layer conversion")
			}
			if err := hcsshim.ConvertToBaseLayer(snapshotDir); err != nil {
				return 0, errors.Wrap(err, "failed to convert linux files to windows base layer")
			}
			if err := writeCrossOSMarker(snapshotDir); err != nil {
				return 0, errors.Wrap(err, "failed to write cross-OS marker")
			}
			return size, nil
		}
		return 0, err
	}
	if err := writeCrossOSMarker(snapshotDir); err != nil {
		return 0, errors.Wrap(err, "failed to write cross-OS marker")
	}
	return size, nil
}

// crossOSLinuxMarker is written into a snapshot to signal that it stores Linux
// content wrapped into the Windows Files/+Hives/+VHDs layout. The localmounter
// uses it to bypass HCS on reads (HCS PrepareLayer needs Hyper-V for Linux
// content). It sits alongside the known layer files, so HCS ignores it.
const crossOSLinuxMarker = ".cross-os-linux"

// writeCrossOSMarker stamps the snapshot dir so that consumers can
// distinguish a cross-OS Linux source layer from a native Windows layer.
func writeCrossOSMarker(snapshotDir string) error {
	return os.WriteFile(filepath.Join(snapshotDir, crossOSLinuxMarker), nil, 0o644)
}

// isProcessBaseLayerError checks if the error is from ProcessBaseLayer.
func isProcessBaseLayerError(err error) bool {
	return strings.Contains(err.Error(), "ProcessBaseLayer")
}

// hasFilesDir checks if the Files/ directory exists in the snapshot.
func hasFilesDir(snapshotDir string) bool {
	_, err := os.Stat(filepath.Join(snapshotDir, "Files"))
	return err == nil
}

// removeAllExceptFiles deletes everything in snapshotDir except the Files/
// tree, giving ConvertToBaseLayer the clean candidate it requires.
func removeAllExceptFiles(ctx context.Context, snapshotDir string) error {
	entries, err := os.ReadDir(snapshotDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name() == "Files" {
			bklog.G(ctx).Debugf("removeAllExceptFiles: keeping %s (dir=%v)", e.Name(), e.IsDir())
			continue
		}
		bklog.G(ctx).Debugf("removeAllExceptFiles: removing %s (dir=%v)", e.Name(), e.IsDir())
		if err := os.RemoveAll(filepath.Join(snapshotDir, e.Name())); err != nil {
			return errors.Wrapf(err, "removing %s", e.Name())
		}
	}
	return nil
}

// getParentLayerPaths extracts parent layer paths from Windows mount options.
func getParentLayerPaths(mounts []mount.Mount) []string {
	for _, m := range mounts {
		for _, opt := range m.Options {
			if after, ok := strings.CutPrefix(opt, "parentLayerPaths="); ok {
				var paths []string
				if err := json.Unmarshal([]byte(after), &paths); err == nil {
					return paths
				}
			}
		}
	}
	return nil
}
