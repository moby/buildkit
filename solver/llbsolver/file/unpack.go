package file

import (
	"archive/tar"
	"context"
	"os"
	"time"

	"github.com/containerd/continuity/fs"
	"github.com/moby/buildkit/util/archiveutil"
	"github.com/moby/sys/user"
	copy "github.com/tonistiigi/fsutil/copy"
)

func unpack(ctx context.Context, srcRoot string, src string, destRoot string, dest string, ch copy.Chowner, u *copy.User, tm *time.Time, idmap *user.IdentityMapping) (bool, error) {
	src, err := fs.RootPath(srcRoot, src)
	if err != nil {
		return false, err
	}
	if !isArchivePath(src) {
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

	rdr, err := archiveutil.DecompressStream(file)
	if err != nil {
		return false, err
	}
	defer rdr.Close()

	err = applyRootArchive(ctx, dest, rdr, u, idmap, unpackNoSameOwner())
	return true, err
}

func isArchivePath(path string) bool {
	fi, err := os.Lstat(path)
	if err != nil {
		return false
	}
	if fi.Mode()&os.ModeType != 0 {
		return false
	}
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	rdr, err := archiveutil.DecompressStream(file)
	if err != nil {
		return false
	}
	defer rdr.Close()
	r := tar.NewReader(rdr)
	_, err = r.Next()
	return err == nil
}
