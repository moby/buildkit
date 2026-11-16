//go:build !windows

package mounts

import (
	"os"
	"syscall"

	"github.com/pkg/errors"
	"golang.org/x/sys/unix"
)

func hostCacheMountsSupported() error {
	return nil
}

// tryLockFile takes an advisory flock(2) lock without blocking and reports
// whether it was acquired.
func tryLockFile(f *os.File, exclusive bool) (bool, error) {
	how := unix.LOCK_SH
	if exclusive {
		how = unix.LOCK_EX
	}
	for {
		err := unix.Flock(int(f.Fd()), how|unix.LOCK_NB)
		switch {
		case err == nil:
			return true, nil
		case errors.Is(err, unix.EWOULDBLOCK):
			return false, nil
		case errors.Is(err, unix.EINTR):
			continue
		default:
			return false, errors.Wrapf(err, "failed to lock %s", f.Name())
		}
	}
}

func unlockFile(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_UN)
}

func fileOwnerAndMode(fi os.FileInfo) (uid, gid int, mode uint32, _ error) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, 0, errors.Errorf("unsupported file info %T", fi.Sys())
	}
	return int(st.Uid), int(st.Gid), uint32(st.Mode) & 0o7777, nil
}

func applyOwnerAndMode(p string, uid, gid int, mode uint32) error {
	if err := os.Lchown(p, uid, gid); err != nil {
		return err
	}
	// Chmod after chown: chown clears setuid/setgid bits.
	return unix.Chmod(p, mode)
}
