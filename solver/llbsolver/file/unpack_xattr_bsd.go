//go:build darwin || freebsd || netbsd

package file

import (
	"os"

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
