package file

import (
	"os"
	"path/filepath"
	"strconv"

	"github.com/pkg/errors"
	"golang.org/x/sys/unix"
)

func chmodRootFallback(parent *os.File, name string, perm uint32) error {
	// chmod does not require read access. Pin the leaf without opening it for
	// I/O, then chmod through procfs because Fchmod cannot use an O_PATH fd.
	fd, err := unix.Openat(int(parent.Fd()), filepath.Base(name), unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return &os.PathError{Op: "openat", Path: name, Err: err}
	}
	defer unix.Close(fd)

	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return &os.PathError{Op: "fstat", Path: name, Err: err}
	}
	// O_PATH|O_NOFOLLOW can open the symlink itself. Do not chmod its target.
	if st.Mode&unix.S_IFMT == unix.S_IFLNK {
		return &os.PathError{Op: "chmod", Path: name, Err: unix.ELOOP}
	}
	if err := unix.Chmod("/proc/self/fd/"+strconv.Itoa(fd), perm); err != nil {
		return &os.PathError{Op: "chmod", Path: name, Err: errors.Wrap(err, "cannot restore archive metadata through /proc/self/fd")}
	}
	return nil
}
