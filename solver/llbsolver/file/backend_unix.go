//go:build !windows

package file

import (
	"context"
	"os"
	"syscall"

	"github.com/moby/sys/user"
	"github.com/pkg/errors"
	copy "github.com/tonistiigi/fsutil/copy"
	"golang.org/x/sys/unix"
)

func mapUserToChowner(user *copy.User, idmap *user.IdentityMapping) (copy.Chowner, error) {
	if user == nil {
		return func(old *copy.User) (*copy.User, error) {
			if old == nil {
				if idmap == nil {
					return nil, nil
				}
				old = &copy.User{} // root
				// non-nil old is already mapped
				if idmap != nil {
					uid, gid, err := idmap.ToHost(old.UID, old.GID)
					if err != nil {
						return nil, errors.WithStack(err)
					}
					return &copy.User{UID: uid, GID: gid}, nil
				}
			}
			return old, nil
		}, nil
	}
	u := *user
	if idmap != nil {
		uid, gid, err := idmap.ToHost(user.UID, user.GID)
		if err != nil {
			return nil, errors.WithStack(err)
		}
		u.UID = uid
		u.GID = gid
	}
	return func(*copy.User) (*copy.User, error) {
		return &u, nil
	}, nil
}

func platformCopy(ctx context.Context, srcRoot string, src string, destRoot string, dest string, opt ...copy.Opt) error {
	return copy.Copy(ctx, srcRoot, src, destRoot, dest, opt...)
}

func snapshotDirInfo(p string) (os.FileInfo, error) {
	fi, err := os.Lstat(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
			return nil, nil
		}
		return nil, errors.WithStack(err)
	}
	if !fi.IsDir() {
		return nil, nil
	}
	return fi, nil
}

func restoreModTimeOnly(p string, prevFi os.FileInfo) error {
	curFi, err := os.Lstat(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
			return nil
		}
		return errors.Wrapf(err, "failed to stat %s", p)
	}
	if !curFi.IsDir() || !os.SameFile(prevFi, curFi) {
		return nil
	}
	prevMtime := copy.StatMtime(prevFi.Sys().(*syscall.Stat_t))
	curMtime := copy.StatMtime(curFi.Sys().(*syscall.Stat_t))
	if curMtime == prevMtime {
		return nil
	}
	timespec := []unix.Timespec{
		{Sec: utimeOmit, Nsec: utimeOmit},
		{Sec: prevMtime.Sec, Nsec: prevMtime.Nsec},
	}
	if err := unix.UtimesNanoAt(unix.AT_FDCWD, p, timespec, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
			return nil
		}
		return errors.Wrapf(err, "failed to utime %s", p)
	}
	return nil
}
