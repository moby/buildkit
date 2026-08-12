package file

import (
	"archive/tar"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/moby/sys/user"
	"github.com/pkg/errors"
	copy "github.com/tonistiigi/fsutil/copy"
)

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
			if err := root.MkdirAll(parent, 0o755); err != nil {
				return err
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
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := root.Mkdir(opName, mode.Perm()); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
			if err := applyRootOwner(root, opName, hdr, noSameOwner); err != nil {
				return err
			}
			if err := root.Chmod(opName, mode); err != nil {
				return err
			}
			dirs = append(dirs, rootDirTime{
				name:  opName,
				atime: headerAccessTime(hdr),
				mtime: hdr.ModTime,
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
			if err := file.Chmod(mode); err != nil {
				file.Close()
				return err
			}
			if err := root.Chtimes(opName, headerAccessTime(hdr), hdr.ModTime); err != nil {
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
				if err := root.Chmod(opName, mode); err != nil {
					return err
				}
				if err := root.Chtimes(opName, headerAccessTime(hdr), hdr.ModTime); err != nil {
					return err
				}
			}
		default:
			return errors.Errorf("unsupported tar entry %q type %d", hdr.Name, hdr.Typeflag)
		}
	}

	for i := len(dirs) - 1; i >= 0; i-- {
		if err := root.Chtimes(dirs[i].name, dirs[i].atime, dirs[i].mtime); err != nil {
			return err
		}
	}
	return nil
}

func cleanRootTarPath(name string) (string, error) {
	name = filepath.Clean(name)
	if filepath.VolumeName(name) != "" {
		return "", errors.Errorf("archive path %q is outside extraction root", name)
	}
	// Match chroot-style extraction: /foo is extracted as foo under the
	// destination root, while paths that still escape are rejected below.
	name = strings.TrimLeft(name, string(filepath.Separator))
	if name == "." || name == "" {
		return "", nil
	}
	if !filepath.IsLocal(name) {
		return "", errors.Errorf("archive path %q is outside extraction root", name)
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

func headerAccessTime(hdr *tar.Header) time.Time {
	if !hdr.AccessTime.IsZero() {
		return hdr.AccessTime
	}
	return hdr.ModTime
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
