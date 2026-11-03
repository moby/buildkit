package file

import (
	"os"
	"path/filepath"
	"strconv"

	"github.com/pkg/errors"
	"golang.org/x/sys/unix"
)

func setRootSymlinkXattr(root *os.Root, name, key string, value []byte) error {
	parent, err := root.OpenFile(filepath.Dir(name), unix.O_PATH|unix.O_DIRECTORY, 0)
	if err != nil {
		return err
	}
	defer parent.Close()

	// Resolve the parent through os.Root, then address only the final basename.
	// Lsetxattr must not follow that component, even if the link points outside.
	path := "/proc/self/fd/" + strconv.Itoa(int(parent.Fd())) + "/" + filepath.Base(name)
	if err := unix.Lsetxattr(path, key, value, 0); err != nil {
		if isBestEffortRootXattrError(err) {
			return nil
		}
		return &os.PathError{Op: "lsetxattr", Path: name, Err: errors.Wrap(err, "cannot restore archive metadata through /proc/self/fd")}
	}
	return nil
}

func setRootXattr(root *os.Root, file *os.File, name, key string, value []byte) error {
	if file != nil {
		return setRootXattrDefault(root, file, name, key, value)
	}

	file, err := root.OpenFile(name, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err == nil {
		defer file.Close()
		return setRootXattrDefault(root, file, name, key, value)
	}
	if isBestEffortRootXattrError(err) {
		return nil
	}
	if !errors.Is(err, unix.EACCES) {
		return err
	}

	// Setting xattrs does not require read permission. O_PATH
	// pins the inode without opening it for reading; Fsetxattr cannot use such
	// descriptors, so operate through procfs without re-resolving name.
	file, err = root.OpenFile(name, unix.O_PATH|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	fi, err := file.Stat()
	if err != nil {
		return err
	}
	// Unlike an ordinary O_NOFOLLOW open, O_PATH can open the symlink itself.
	if fi.Mode()&os.ModeSymlink != 0 {
		return &os.PathError{Op: "setxattr", Path: name, Err: unix.ELOOP}
	}
	if err := unix.Setxattr("/proc/self/fd/"+strconv.Itoa(int(file.Fd())), key, value, 0); err != nil {
		if isBestEffortRootXattrError(err) {
			return nil
		}
		return &os.PathError{Op: "setxattr", Path: name, Err: errors.Wrap(err, "cannot restore archive metadata through /proc/self/fd")}
	}
	return nil
}
