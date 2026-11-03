//go:build !linux && !windows

package file

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func chmodRootFallback(parent *os.File, name string, perm uint32) error {
	fd, err := unix.Openat(int(parent.Fd()), filepath.Base(name), unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return &os.PathError{Op: "openat", Path: name, Err: err}
	}
	defer unix.Close(fd)

	if err := unix.Fchmod(fd, perm); err != nil {
		return &os.PathError{Op: "fchmod", Path: name, Err: err}
	}
	return nil
}
