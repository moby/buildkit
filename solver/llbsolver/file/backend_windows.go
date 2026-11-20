package file

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/moby/buildkit/util/windows"
	"github.com/moby/sys/user"
	"github.com/pkg/errors"
	copy "github.com/tonistiigi/fsutil/copy"
	syswindows "golang.org/x/sys/windows"
)

func mapUserToChowner(user *copy.User, _ *user.IdentityMapping) (copy.Chowner, error) {
	if user == nil || user.SID == "" {
		return func(old *copy.User) (*copy.User, error) {
			if old == nil || old.SID == "" {
				old = &copy.User{
					SID: windows.ContainerAdministratorSidString,
				}
			}
			return old, nil
		}, nil
	}
	return func(*copy.User) (*copy.User, error) {
		return user, nil
	}, nil
}

// platformCopy wraps copy.Copy to exclude Windows protected system folders.
// On Windows, container snapshots mounted to the host filesystem include protected folders
// ("System Volume Information" and "WcSandboxState") at the mount root, which cause "Access is denied"
// errors. With the fsutil fix, these are excluded before os.Lstat() is called.
func platformCopy(ctx context.Context, srcRoot string, src string, destRoot string, dest string, opt ...copy.Opt) error {
	// Only exclude protected folders when copying from the mount root.
	if filepath.Clean(src) == string(filepath.Separator) {
		opt = append(opt,
			copy.WithExcludePattern("System Volume Information"),
			copy.WithExcludePattern("WcSandboxState"),
		)
	}
	return copy.Copy(ctx, srcRoot, src, destRoot, dest, opt...)
}

func longPathUTF16(path string) ([]uint16, error) {
	if strings.HasPrefix(path, `\\?\`) || strings.HasPrefix(path, `\??\`) || strings.HasPrefix(path, `\\.\`) {
		return syswindows.UTF16FromString(path)
	}
	in, err := syswindows.UTF16FromString(path)
	if err != nil {
		return nil, err
	}
	n := uint32(len(in))
	var full []uint16
	for {
		buf := make([]uint16, n)
		r, err := syswindows.GetFullPathName(&in[0], n, &buf[0], nil)
		if err != nil {
			return nil, err
		}
		if r < n {
			full = buf[:r]
			break
		}
		n = r
	}
	abs := syswindows.UTF16ToString(full)
	if len(abs) < 248 {
		return syswindows.UTF16FromString(abs)
	}
	if strings.HasPrefix(abs, `\\`) {
		return syswindows.UTF16FromString(`\\?\UNC\` + abs[2:])
	}
	return syswindows.UTF16FromString(`\\?\` + abs)
}

func snapshotDirInfo(p string) (os.FileInfo, error) {
	fullPath, err := longPathUTF16(p)
	if err != nil {
		return nil, errors.Wrap(err, "converting to utf16 path")
	}
	h, err := syswindows.CreateFile(
		&fullPath[0],
		syswindows.FILE_READ_ATTRIBUTES,
		syswindows.FILE_SHARE_READ|syswindows.FILE_SHARE_WRITE|syswindows.FILE_SHARE_DELETE,
		nil,
		syswindows.OPEN_EXISTING,
		syswindows.FILE_FLAG_BACKUP_SEMANTICS|syswindows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) || errors.Is(err, os.ErrPermission) {
			return nil, nil
		}
		return nil, errors.WithStack(err)
	}
	f := os.NewFile(uintptr(h), p)
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return nil, errors.WithStack(err)
	}
	if !fi.IsDir() {
		return nil, nil
	}
	return fi, nil
}

func restoreModTimeOnly(p string, prevFi os.FileInfo) error {
	rawPrev, ok := prevFi.Sys().(*syscall.Win32FileAttributeData)
	if !ok {
		return nil
	}
	curFi, err := snapshotDirInfo(p)
	if err != nil || curFi == nil || !os.SameFile(prevFi, curFi) {
		return err
	}
	if rawCur, ok := curFi.Sys().(*syscall.Win32FileAttributeData); ok && rawCur.LastWriteTime == rawPrev.LastWriteTime {
		return nil
	}
	fullPath, err := longPathUTF16(p)
	if err != nil {
		return errors.Wrap(err, "converting to utf16 path")
	}
	h, err := syswindows.CreateFile(
		&fullPath[0],
		syswindows.FILE_READ_ATTRIBUTES|syswindows.FILE_WRITE_ATTRIBUTES,
		syswindows.FILE_SHARE_READ|syswindows.FILE_SHARE_WRITE|syswindows.FILE_SHARE_DELETE,
		nil,
		syswindows.OPEN_EXISTING,
		syswindows.FILE_FLAG_BACKUP_SEMANTICS|syswindows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) || errors.Is(err, os.ErrPermission) {
			return nil
		}
		return errors.Wrap(err, "opening handle to restore mtime")
	}
	f := os.NewFile(uintptr(h), p)
	defer f.Close()

	recheckFi, err := f.Stat()
	if err != nil {
		return errors.Wrap(err, "reading handle attributes")
	}
	if !recheckFi.IsDir() || !os.SameFile(prevFi, recheckFi) {
		return nil
	}
	ft := syswindows.Filetime{
		LowDateTime:  rawPrev.LastWriteTime.LowDateTime,
		HighDateTime: rawPrev.LastWriteTime.HighDateTime,
	}
	if err := syswindows.SetFileTime(h, nil, nil, &ft); err != nil {
		if errors.Is(err, os.ErrPermission) {
			return nil
		}
		return errors.Wrap(err, "restoring mtime")
	}
	return nil
}
