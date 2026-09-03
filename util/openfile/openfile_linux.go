package openfile

import (
	"os"
	"strconv"

	"github.com/pkg/errors"
	"golang.org/x/sys/unix"
)

// openRegular pins the inode with O_PATH before deciding what it is. O_PATH
// does not call the driver's open method, so a device node is refused without
// the host device ever being opened. The pinned descriptor is then reopened
// through /proc/self/fd, so the file that is read is the inode that was
// checked, leaving no window for the path to be swapped. O_NOFOLLOW is safe
// because callers pass a path that is already fully resolved.
func openRegular(p string) (*os.File, error) {
	pinned, err := os.OpenFile(p, unix.O_PATH|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	defer pinned.Close()

	if err := checkRegular(pinned, p); err != nil {
		return nil, err
	}

	f, err := os.Open("/proc/self/fd/" + strconv.Itoa(int(pinned.Fd())))
	if err != nil {
		var pathErr *os.PathError
		if errors.As(err, &pathErr) {
			pathErr.Path = p
		}
		return nil, errors.WithStack(err)
	}
	return f, nil
}
