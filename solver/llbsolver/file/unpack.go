package file

import (
	"archive/tar"
	"context"
	stderrors "errors"
	"os"
	"os/exec"
	"time"

	"github.com/containerd/continuity/fs"
	"github.com/moby/buildkit/util/compression"
	"github.com/moby/sys/user"
	copy "github.com/tonistiigi/fsutil/copy"
)

func unpack(ctx context.Context, srcRoot string, src string, destRoot string, dest string, ch copy.Chowner, u *copy.User, tm *time.Time, idmap *user.IdentityMapping) (bool, error) {
	src, err := fs.RootPath(srcRoot, src)
	if err != nil {
		return false, err
	}
	isArchive, err := isArchivePath(src)
	if err != nil {
		return false, err
	}
	if !isArchive {
		return false, nil
	}

	dest, err = fs.RootPath(destRoot, dest)
	if err != nil {
		return false, err
	}
	if _, err := copy.MkdirAll(dest, 0755, ch, tm); err != nil {
		return false, err
	}

	file, err := os.Open(src)
	if err != nil {
		return false, err
	}
	defer file.Close()

	rdr, err := compression.DecompressStream(file)
	if err != nil {
		return false, err
	}
	defer rdr.Close()

	err = applyRootArchive(ctx, dest, rdr, u, idmap, unpackNoSameOwner())
	return true, err
}

func isArchivePath(path string) (bool, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return false, nil
	}
	if fi.Mode()&os.ModeType != 0 {
		return false, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return false, nil
	}
	defer file.Close()
	rdr, err := compression.DecompressStream(file)
	if err != nil {
		if _, ok := stderrors.AsType[*exec.Error](err); ok {
			return false, err
		}
		return false, nil
	}
	defer rdr.Close()
	r := tar.NewReader(rdr)
	_, err = r.Next()
	return err == nil, nil
}
