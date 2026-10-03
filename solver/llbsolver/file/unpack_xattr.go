//go:build linux || darwin || freebsd || netbsd

package file

import (
	"os"
	"syscall"

	"github.com/pkg/errors"
	"golang.org/x/sys/unix"
)

func setRootXattrDefault(root *os.Root, file *os.File, name, key string, value []byte) error {
	if file == nil {
		// os.Root has no xattr method. Open the path through the root first,
		// then set xattrs through the fd to preserve the containment boundary.
		var err error
		file, err = root.OpenFile(name, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
		if err != nil {
			if isBestEffortRootXattrError(err) {
				return nil
			}
			return err
		}
		defer file.Close()
	}
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
