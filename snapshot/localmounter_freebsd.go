package snapshot

import (
	"fmt"
	"os"
	"slices"

	"github.com/containerd/containerd/v2/core/mount"
)

func (lm *localMounter) Mount() (string, error) {
	lm.mu.Lock()
	defer lm.mu.Unlock()

	if lm.mounts == nil && lm.mountable != nil {
		mounts, release, err := lm.mountable.Mount()
		if err != nil {
			return "", err
		}
		lm.mounts = mounts
		lm.release = release
	}

	if !lm.forceRemount && len(lm.mounts) == 1 && lm.mounts[0].Type == "nullfs" {
		ro := slices.Contains(lm.mounts[0].Options, "ro")
		if !ro {
			return lm.mounts[0].Source, nil
		}
	}

	dir, err := os.MkdirTemp("", "buildkit-mount")
	if err != nil {
		return "", fmt.Errorf("failed to create temp dir"+": %w", err)
	}

	if err := mount.All(lm.mounts, dir); err != nil {
		os.RemoveAll(dir)
		return "", fmt.Errorf("failed to mount %s: %+v: %w", dir, lm.mounts, err)
	}
	lm.target = dir
	return dir, nil
}

func (lm *localMounter) Unmount() error {
	lm.mu.Lock()
	defer lm.mu.Unlock()

	if lm.target != "" {
		if err := mount.Unmount(lm.target, 0); err != nil {
			return err
		}
		os.RemoveAll(lm.target)
		lm.target = ""
	}

	if lm.release != nil {
		return lm.release()
	}

	return nil
}
