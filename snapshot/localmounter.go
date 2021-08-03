package snapshot

import (
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

// RWDirMount extracts out just a writable directory from the provided mounts and returns it.
// It's intended to supply the directory to which changes being made to the mount can be
// written directly. A writable directory includes an upperdir if provided an overlay or a rw
// bind mount source. If the mount doesn't have a writable directory, an error is returned.
func getRWDir(mounts []mount.Mount) (string, error) {
	if len(mounts) != 1 {
		return "", errors.New("cannot extract writable directory from zero or multiple mounts")
	}
	mnt := mounts[0]
	switch mnt.Type {
	case "overlay":
		for _, opt := range mnt.Options {
			if strings.HasPrefix(opt, "upperdir=") {
				upperdir := strings.SplitN(opt, "=", 2)[1]
				return upperdir, nil
			}
		}
		return "", errors.New("cannot extract writable directory from overlay mount without upperdir")
	case "bind", "rbind":
		for _, opt := range mnt.Options {
			if opt == "ro" {
				return "", errors.New("cannot extract writable directory from read-only bind mount")
			}
		}
		return mnt.Source, nil
	default:
		return "", errors.Errorf("cannot extract writable directory from unhandled mount type %q", mnt.Type)
	}
}
