package file

import (
	"archive/tar"
	"context"
	"os"
	"time"

	"github.com/containerd/containerd/v2/pkg/archive"
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

	opts := []archive.ApplyOpt{
		// Disable containerd's image-layer whiteout conversion for Dockerfile ADD
		// archives, where .wh.* entries should be extracted as files.
		archive.WithConvertWhiteout(func(_ *tar.Header, _ string) (bool, error) {
			return true, nil
		}),
		archive.WithFilter(ownerMapper(u, idmap)),
	}
	opts = append(opts, unpackPlatformApplyOpts()...)

	_, err = archive.Apply(ctx, dest, rdr, opts...)
	return true, err
}

func ownerMapper(u *copy.User, idmap *user.IdentityMapping) archive.Filter {
	return func(hdr *tar.Header) (bool, error) {
		uid, gid := hdr.Uid, hdr.Gid
		// Match go-archive behavior: remap archive header IDs first, then let
		// explicit --chown values override the header ownership.
		if idmap != nil {
			var err error
			uid, gid, err = idmap.ToHost(uid, gid)
			if err != nil {
				return false, err
			}
		}
		if u != nil {
			uid, gid = u.UID, u.GID
		}
		hdr.Uid, hdr.Gid = uid, gid
		return true, nil
	}
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
