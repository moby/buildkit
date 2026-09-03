//go:build linux || darwin || freebsd || netbsd

package file

import (
	"errors"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func setRootXattr(root *os.Root, file *os.File, name, key string, value []byte) error {
	if file != nil {
		return fsetRootXattr(file, name, key, value)
	}

	// os.Root has no xattr method. Open the path through the root first, then
	// set xattrs through the fd so xattrs keep the same containment boundary.
	file, err := root.OpenFile(name, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		if isBestEffortRootXattrError(err) {
			return nil
		}
		return err
	}
	defer file.Close()

	return fsetRootXattr(file, name, key, value)
}

func fsetRootXattr(file *os.File, name, key string, value []byte) error {
	if err := unix.Fsetxattr(int(file.Fd()), key, value, 0); err != nil {
		if isBestEffortRootXattrError(err) {
			return nil
		}
		return &os.PathError{Op: "fsetxattr", Path: name, Err: err}
	}
	return nil
}

func isBestEffortRootXattrError(err error) bool {
	return errors.Is(err, syscall.ENOTSUP) ||
		errors.Is(err, syscall.EOPNOTSUPP) ||
		errors.Is(err, syscall.EPERM)
}
