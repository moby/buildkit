package file

import (
	"archive/tar"
	"errors"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"time"

	"github.com/containerd/continuity/fs"
	"github.com/moby/go-archive"
	"github.com/moby/go-archive/chrootarchive"
	"github.com/moby/go-archive/compression"
	"github.com/moby/sys/user"
	copy "github.com/tonistiigi/fsutil/copy"
)

// unpack extracts the archive src to dest. tm sets the timestamps of the
// directories created for dest. With onlyCreatedDirs, it also sets the
// timestamps of the directories of the archive that have no entry in it and
// would otherwise get the time of the extraction.
func unpack(srcRoot string, src string, destRoot string, dest string, ch copy.Chowner, u *copy.User, tm *time.Time, onlyCreatedDirs bool, idmap *user.IdentityMapping) (bool, error) {
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
	createdDirs, err := copy.MkdirAll(dest, 0755, ch, tm)
	if err != nil {
		return false, err
	}

	var implicitDirs []string
	if tm != nil && onlyCreatedDirs {
		implicitDirs, err = archiveImplicitDirs(src, dest)
		if err != nil {
			return false, err
		}
	}

	file, err := os.Open(src)
	if err != nil {
		return false, err
	}
	defer file.Close()

	opts := &archive.TarOptions{
		BestEffortXattrs: true,
	}
	if idmap != nil {
		opts.IDMap = *idmap
	}
	if u != nil {
		opts.ChownOpts = &archive.ChownOpts{
			UID: u.UID,
			GID: u.GID,
		}
	}
	if err := chrootarchive.Untar(file, dest, opts); err != nil {
		return true, err
	}
	// Creating the directories one after the other, then extracting the
	// archive in the last one, updated their modification times: set them
	// again.
	if tm != nil {
		for _, d := range createdDirs {
			if err := copy.Utimes(d, tm); err != nil {
				return true, err
			}
		}
	}
	for _, d := range implicitDirs {
		if fi, err := os.Lstat(d); err != nil || !fi.IsDir() {
			continue
		}
		if err := copy.Utimes(d, tm); err != nil {
			return true, err
		}
	}
	return true, nil
}

// archiveImplicitDirs returns the directories that extracting the archive src
// to dest creates without a corresponding entry in the archive: the missing
// parent directories of its entries.
func archiveImplicitDirs(src, dest string) ([]string, error) {
	file, err := os.Open(src)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	rdr, err := compression.DecompressStream(file)
	if err != nil {
		return nil, err
	}
	defer rdr.Close()

	explicit := map[string]struct{}{}
	parents := map[string]struct{}{}
	tr := tar.NewReader(rdr)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		name := path.Clean("/" + filepath.ToSlash(hdr.Name))
		if hdr.Typeflag == tar.TypeDir {
			explicit[name] = struct{}{}
		}
		for p := path.Dir(name); p != "/"; p = path.Dir(p) {
			if _, ok := parents[p]; ok {
				break
			}
			parents[p] = struct{}{}
		}
	}

	var dirs []string
	for p := range parents {
		if _, ok := explicit[p]; ok {
			continue
		}
		d, err := fs.RootPath(dest, p)
		if err != nil {
			return nil, err
		}
		if _, err := os.Lstat(d); errors.Is(err, os.ErrNotExist) {
			dirs = append(dirs, d)
		}
	}
	return dirs, nil
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
		if _, ok := errors.AsType[*exec.Error](err); ok {
			return false, err
		}
		return false, nil
	}
	defer rdr.Close()
	r := tar.NewReader(rdr)
	_, err = r.Next()
	return err == nil, nil
}
