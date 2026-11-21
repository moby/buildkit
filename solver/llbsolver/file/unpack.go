package file

import (
	"archive/tar"
	stderrors "errors"
	"io"
	"os"
	"os/exec"
	"path"
	"strings"
	"time"

	"github.com/containerd/continuity/fs"
	"github.com/moby/go-archive"
	"github.com/moby/go-archive/chrootarchive"
	"github.com/moby/go-archive/compression"
	"github.com/moby/patternmatcher"
	"github.com/moby/sys/user"
	copy "github.com/tonistiigi/fsutil/copy"
)

func unpack(srcRoot string, src string, destRoot string, dest string, ch copy.Chowner, u *copy.User, tm *time.Time, idmap *user.IdentityMapping, excludePatterns []string) (bool, error) {
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

	if len(excludePatterns) > 0 {
		matcher, err := patternmatcher.New(excludePatterns)
		if err != nil {
			return false, err
		}
		// go-archive's Unpack only matches ExcludePatterns as plain
		// path prefixes, which cannot express the Dockerfile
		// --exclude globs (e.g. go/doc/**). Filter the tar stream
		// ourselves with the same patternmatcher the regular copy
		// path uses, matching entries against their paths inside
		// the archive, then feed the filtered stream to the
		// uncompressed untar path.
		decompressed, err := compression.DecompressStream(file)
		if err != nil {
			return false, err
		}
		defer decompressed.Close()
		return true, chrootarchive.UntarUncompressed(filterTar(decompressed, matcher), dest, opts)
	}

	return true, chrootarchive.Untar(file, dest, opts)
}

// filterTar returns a reader over the tar stream in r with every entry
// whose archive-relative path matches matcher removed. Entries are
// matched with the same semantics as COPY --exclude: a pattern matches
// a file or a parent directory of it (so excluding a directory
// excludes its whole subtree).
func filterTar(r io.Reader, matcher *patternmatcher.PatternMatcher) io.Reader {
	pr, pw := io.Pipe()
	go func() {
		pw.CloseWithError(copyTarFiltered(r, pw, matcher))
	}()
	return pr
}

func copyTarFiltered(r io.Reader, w io.Writer, matcher *patternmatcher.PatternMatcher) error {
	tr := tar.NewReader(r)
	tw := tar.NewWriter(w)

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		name := strings.TrimPrefix(path.Clean(hdr.Name), "/")
		excluded, err := matcher.MatchesOrParentMatches(name)
		if err != nil {
			return err
		}
		if excluded {
			continue
		}

		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if _, err := io.Copy(tw, tr); err != nil {
			return err
		}
	}

	return tw.Close()
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
