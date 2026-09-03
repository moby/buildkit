package file

import (
	"archive/tar"
	"context"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/moby/sys/user"
	"github.com/pkg/errors"
	copy "github.com/tonistiigi/fsutil/copy"
)

var (
	minRootTime = time.Unix(0, 0)
	maxRootTime time.Time
)

func init() {
	if unsafe.Sizeof(syscall.Timespec{}.Nsec) == 8 {
		maxRootTime = time.Unix(0, 1<<63-1)
	} else {
		maxRootTime = time.Unix(1<<31-1, 0)
	}
}

type rootDirTime struct {
	name  string
	atime time.Time
	mtime time.Time
}

func applyRootArchive(ctx context.Context, dest string, r io.Reader, u *copy.User, idmap *user.IdentityMapping, noSameOwner bool) error {
	root, err := os.OpenRoot(dest)
	if err != nil {
		return err
	}
	defer root.Close()

	tr := tar.NewReader(r)
	var dirs []rootDirTime
	var copyBuf []byte
	impliedUID, impliedGID := 0, 0
	if idmap != nil {
		impliedUID, impliedGID = idmap.RootPair()
	}
	for {
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		default:
		}

		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag == tar.TypeXGlobalHeader || hdr.Typeflag == tar.TypeXHeader {
			continue
		}

		name, err := cleanRootTarPath(hdr.Name)
		if err != nil {
			return err
		}
		if name == "" {
			continue
		}
		hdr.Name = name

		// Dockerfile ADD extracts filesystem content, but should not create
		// device nodes or FIFOs. Skip them without replacing existing paths.
		switch hdr.Typeflag {
		case tar.TypeBlock, tar.TypeChar, tar.TypeFifo:
			continue
		}

		if err := mapArchiveHeaderOwner(hdr, u, idmap); err != nil {
			return err
		}

		opName, err := resolveRootPath(root, name, hdr.Typeflag == tar.TypeDir)
		if err != nil {
			return err
		}
		if opName == "" {
			continue
		}

		parent := filepath.Dir(opName)
		if parent != "." {
			var cur string
			for c := range strings.SplitSeq(parent, string(filepath.Separator)) {
				if c == "" {
					continue
				}
				cur = filepath.Join(cur, c)
				if err := root.Mkdir(cur, 0o755); err != nil {
					if !errors.Is(err, os.ErrExist) {
						return err
					}
					fi, err := root.Stat(cur)
					if err != nil {
						return err
					}
					if fi.IsDir() {
						continue
					}
					return &os.PathError{Op: "mkdir", Path: cur, Err: syscall.ENOTDIR}
				}
				if !noSameOwner {
					if err := root.Lchown(cur, impliedUID, impliedGID); err != nil {
						return err
					}
				}
				if err := applyRootMode(root, cur, 0o755); err != nil {
					return err
				}
			}
		}

		fi, err := root.Lstat(opName)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return err
			}
		} else if !fi.IsDir() || hdr.Typeflag != tar.TypeDir {
			if err := root.RemoveAll(opName); err != nil {
				return err
			}
		}

		mode := hdr.FileInfo().Mode()
		atime, mtime := rootHeaderTimes(hdr)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := root.Mkdir(opName, mode.Perm()); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
			if err := applyRootOwner(root, opName, hdr, noSameOwner); err != nil {
				return err
			}
			if err := applyRootXattrs(root, nil, opName, hdr); err != nil {
				return err
			}
			if err := applyRootMode(root, opName, mode); err != nil {
				return err
			}
			dirs = append(dirs, rootDirTime{
				name:  opName,
				atime: atime,
				mtime: mtime,
			})
		case tar.TypeReg, 0:
			// A zero typeflag is the historic tar regular-file marker.
			file, err := root.OpenFile(opName, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode.Perm())
			if err != nil {
				return err
			}
			if copyBuf == nil {
				copyBuf = make([]byte, 32*1024)
			}
			for {
				select {
				case <-ctx.Done():
					file.Close()
					return context.Cause(ctx)
				default:
				}

				nr, er := tr.Read(copyBuf)
				if nr > 0 {
					nw, ew := file.Write(copyBuf[:nr])
					if ew != nil {
						file.Close()
						return ew
					}
					if nw != nr {
						file.Close()
						return io.ErrShortWrite
					}
				}
				if er == io.EOF {
					break
				}
				if er != nil {
					file.Close()
					return er
				}
			}
			if !noSameOwner {
				if err := file.Chown(hdr.Uid, hdr.Gid); err != nil {
					file.Close()
					return err
				}
			}
			if err := applyRootXattrs(root, file, opName, hdr); err != nil {
				file.Close()
				return err
			}
			if err := file.Chmod(mode); err != nil {
				file.Close()
				return err
			}
			if err := root.Chtimes(opName, atime, mtime); err != nil {
				file.Close()
				return err
			}
			if err := file.Close(); err != nil {
				return err
			}
		case tar.TypeSymlink:
			// Preserve the archive's link target. os.Root does not validate the
			// target here. Later extraction resolves absolute targets relative to
			// the extraction root and rejects relative targets that escape it.
			if err := root.Symlink(hdr.Linkname, opName); err != nil {
				return err
			}
			if err := applyRootOwner(root, opName, hdr, noSameOwner); err != nil {
				return err
			}
			// Avoid applying xattrs through symlinks. os.Root has no non-following
			// xattr method, and opening the path would touch the symlink target.
			if err := applyRootSymlinkTimes(root, opName, atime, mtime); err != nil {
				return err
			}
		case tar.TypeLink:
			linkName, err := cleanRootTarPath(hdr.Linkname)
			if err != nil {
				return err
			}
			if linkName == "" {
				return errors.Errorf("archive hardlink %q targets extraction root", hdr.Name)
			}
			linkName, err = resolveRootPath(root, linkName, false)
			if err != nil {
				return err
			}
			if err := root.Link(linkName, opName); err != nil {
				return err
			}
			if err := applyRootOwner(root, opName, hdr, noSameOwner); err != nil {
				return err
			}
			fi, err := root.Lstat(opName)
			if err != nil {
				return err
			}
			if fi.Mode()&os.ModeSymlink == 0 {
				if err := applyRootXattrs(root, nil, opName, hdr); err != nil {
					return err
				}
				if err := applyRootMode(root, opName, mode); err != nil {
					return err
				}
				if err := root.Chtimes(opName, atime, mtime); err != nil {
					return err
				}
			}
		default:
			return errors.Errorf("unsupported tar entry %q type %d", hdr.Name, hdr.Typeflag)
		}
	}

	for i := range len(dirs) {
		d := dirs[len(dirs)-1-i]
		if err := root.Chtimes(d.name, d.atime, d.mtime); err != nil {
			return err
		}
	}
	return nil
}

func applyRootXattrs(root *os.Root, file *os.File, name string, hdr *tar.Header) error {
	const paxSchilyXattr = "SCHILY.xattr."
	for key, value := range hdr.PAXRecords {
		xattr, ok := strings.CutPrefix(key, paxSchilyXattr)
		if !ok {
			continue
		}
		if err := setRootXattr(root, file, name, xattr, []byte(value)); err != nil {
			return errors.Wrapf(err, "failed to set xattr %q", xattr)
		}
	}
	return nil
}

func cleanRootTarPath(name string) (string, error) {
	original := name
	name = path.Clean(strings.TrimLeft(name, "/"))
	if name == "." || name == "" {
		return "", nil
	}
	if err := validateRootTarPath(name); err != nil {
		return "", errors.Wrapf(err, "archive path %q is outside extraction root", original)
	}
	name = filepath.FromSlash(name)
	if filepath.VolumeName(name) != "" {
		return "", errors.Errorf("archive path %q is outside extraction root", original)
	}
	if !filepath.IsLocal(name) {
		return "", errors.Errorf("archive path %q is outside extraction root", original)
	}
	return name, nil
}

func resolveRootPath(root *os.Root, name string, followLeaf bool) (string, error) {
	original := name
	for range 255 {
		parts := strings.Split(name, string(filepath.Separator))
		resolved := make([]string, 0, len(parts))
		followed := false
		for i, part := range parts {
			if part == "" || part == "." {
				continue
			}
			candidate := filepath.Join(append(resolved, part)...)
			if i == len(parts)-1 && !followLeaf {
				resolved = append(resolved, part)
				continue
			}
			fi, err := root.Lstat(candidate)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					resolved = append(resolved, parts[i:]...)
					return filepath.Join(resolved...), nil
				}
				return "", err
			}
			if fi.Mode()&os.ModeSymlink == 0 {
				resolved = append(resolved, part)
				continue
			}

			target, err := root.Readlink(candidate)
			if err != nil {
				return "", err
			}
			if filepath.IsAbs(target) || filepath.VolumeName(target) != "" {
				name, err = cleanRootTarPath(target)
				if err != nil {
					return "", err
				}
			} else {
				name = filepath.Clean(filepath.Join(filepath.Dir(candidate), target))
				if name == "." {
					name = ""
				}
				if name != "" && !filepath.IsLocal(name) {
					return "", errors.Errorf("archive symlink %q points outside extraction root", candidate)
				}
			}
			remaining := filepath.Join(parts[i+1:]...)
			if remaining != "" {
				name = filepath.Join(name, remaining)
			}
			followed = true
			break
		}
		if !followed {
			return filepath.Join(resolved...), nil
		}
	}
	return "", errors.Errorf("too many symlinks resolving archive path %q", original)
}

func rootHeaderTimes(hdr *tar.Header) (time.Time, time.Time) {
	atime := hdr.ModTime
	if hdr.AccessTime.After(atime) {
		atime = hdr.AccessTime
	}
	return boundRootTime(atime), boundRootTime(hdr.ModTime)
}

func boundRootTime(t time.Time) time.Time {
	if t.Before(minRootTime) || t.After(maxRootTime) {
		return minRootTime
	}
	return t
}

func mapArchiveHeaderOwner(hdr *tar.Header, u *copy.User, idmap *user.IdentityMapping) error {
	uid, gid := hdr.Uid, hdr.Gid
	// Match go-archive behavior: remap archive header IDs first, then let
	// explicit --chown values override the header ownership.
	if idmap != nil {
		var err error
		uid, gid, err = idmap.ToHost(uid, gid)
		if err != nil {
			return err
		}
	}
	if u != nil {
		uid, gid = u.UID, u.GID
	}
	hdr.Uid, hdr.Gid = uid, gid
	return nil
}
