package snapshot

import (
	"context"
	"os"
	"strings"
	"sync"

	"github.com/containerd/containerd/mount"
	"github.com/pkg/errors"
)

type Mounter interface {
	Mount() (string, error)
	Unmount() error
}

// LocalMounter is a helper for mounting mountfactory to temporary path. In
// addition it can mount binds without privileges
func LocalMounter(mountable Mountable) Mounter {
	return &localMounter{mountable: mountable}
}

// LocalMounterWithMounts is a helper for mounting to temporary path. In
// addition it can mount binds without privileges
func LocalMounterWithMounts(mounts []mount.Mount) Mounter {
	return &localMounter{mounts: mounts}
}

type localMounter struct {
	mu        sync.Mutex
	mounts    []mount.Mount
	mountable Mountable
	target    string
	release   func() error
}

// withTempMount is like mount.WithTempMount but avoids actually creating a mount if provided a bind-mount. This is
// useful for running in unit-tests and probably a very slight performance improvement but requires the callers respect
// any read-only flags as they will not be enforced by the bind-mount.
func withTempMount(ctx context.Context, mounts []mount.Mount, f func(root string) error) error {
	if len(mounts) == 0 {
		// TODO: this code is from containerd's mount.WithTempMount, which has a bug where it tries to unmount
		// an empty tmpdir which was never mounted and then gets EPERM, which it doesn't ignore.
		tmpRoot := os.Getenv("XDG_RUNTIME_DIR")
		if tmpRoot == "" {
			tmpRoot = os.TempDir()
		}
		tmpdir, err := os.MkdirTemp(tmpRoot, "buildkitd-mount")
		if err != nil {
			return errors.Wrap(err, "failed to make empty temp dir for nil mount")
		}
		return f(tmpdir)
	}
	if len(mounts) == 1 {
		mnt := mounts[0]
		if mnt.Type == "bind" || mnt.Type == "rbind" {
			return f(mnt.Source)
		}
	}
	return mount.WithTempMount(ctx, mounts, f)
}

// withRWDirMount extracts out just a writable directory from the provided mounts and calls the provided func on
// that. It's intended to supply the directory to which changes being made to the mount can be written directly. A
// writable directory includes an upperdir if provided an overlay or a rw bind mount source.  If the mount doesn't have
// a writable directory, an error is returned.
func withRWDirMount(ctx context.Context, mounts []mount.Mount, f func(root string) error) error {
	if len(mounts) != 1 {
		return errors.New("cannot extract writable directory from zero or multiple mounts")
	}
	mnt := mounts[0]
	switch mnt.Type {
	case "overlay":
		for _, opt := range mnt.Options {
			if strings.HasPrefix(opt, "upperdir=") {
				upperdir := strings.SplitN(opt, "=", 2)[1]
				return f(upperdir)
			}
		}
		return errors.New("cannot extract writable directory from overlay mount without upperdir")
	case "bind", "rbind":
		for _, opt := range mnt.Options {
			if opt == "ro" {
				return errors.New("cannot extract writable directory from read-only bind mount")
			}
		}
		return f(mnt.Source)
	default:
		return errors.Errorf("cannot extract writable directory from unhandled mount type %q", mnt.Type)
	}
}
