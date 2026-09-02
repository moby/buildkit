//go:build linux || darwin || freebsd || netbsd

package file

import (
	"errors"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

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
