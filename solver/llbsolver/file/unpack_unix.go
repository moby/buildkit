//go:build !windows

package file

import (
	"archive/tar"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func unpackNoSameOwner() bool {
	return false
}

func validateRootTarPath(string) error {
	return nil
}

func applyRootOwner(root *os.Root, name string, hdr *tar.Header, noSameOwner bool) error {
	if noSameOwner {
		return nil
	}
	return root.Lchown(name, hdr.Uid, hdr.Gid)
}

func applyRootMode(root *os.Root, name string, mode os.FileMode) error {
	parent, err := root.OpenFile(filepath.Dir(name), os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer parent.Close()

	perm := uint32(mode.Perm())
	if mode&os.ModeSetuid != 0 {
		perm |= unix.S_ISUID
	}
	if mode&os.ModeSetgid != 0 {
		perm |= unix.S_ISGID
	}
	if mode&os.ModeSticky != 0 {
		perm |= unix.S_ISVTX
	}

	base := filepath.Base(name)
	if err := unix.Fchmodat(int(parent.Fd()), base, perm, unix.AT_SYMLINK_NOFOLLOW); err == nil {
		return nil
	} else if !errors.Is(err, syscall.EOPNOTSUPP) {
		return &os.PathError{Op: "fchmodat2", Path: name, Err: err}
	}

	fd, err := unix.Openat(int(parent.Fd()), base, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return &os.PathError{Op: "openat", Path: name, Err: err}
	}
	defer unix.Close(fd)

	if err := unix.Fchmod(fd, perm); err != nil {
		return &os.PathError{Op: "fchmod", Path: name, Err: err}
	}
	return nil
}

func applyRootSymlinkTimes(root *os.Root, name string, atime, mtime time.Time) error {
	parent, err := root.OpenFile(filepath.Dir(name), os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer parent.Close()

	times := []unix.Timespec{
		unix.NsecToTimespec(atime.UnixNano()),
		unix.NsecToTimespec(mtime.UnixNano()),
	}
	if err := unix.UtimesNanoAt(int(parent.Fd()), filepath.Base(name), times, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return &os.PathError{Op: "utimensat", Path: name, Err: err}
	}
	return nil
}
