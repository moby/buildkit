//go:build !windows
// +build !windows

package snapshot

import (
	"context"
	gofs "io/fs"
	"os"
	"path/filepath"
	"syscall"

	"github.com/containerd/containerd/leases"
	"github.com/containerd/containerd/mount"
	"github.com/containerd/containerd/snapshots"
	"github.com/containerd/continuity/fs"
	"github.com/containerd/continuity/sysx"
	"github.com/containerd/stargz-snapshotter/snapshot/overlayutils"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/util/bklog"
	"github.com/moby/buildkit/util/leaseutil"
	"github.com/moby/buildkit/util/overlay"
	"github.com/pkg/errors"
	"golang.org/x/sys/unix"
)

// diffApply calculates the diff between two directories and directly applies the changes to a separate mount (without using
// the content store as an intermediary). If useHardlink is set to true, it will hardlink non-directories instead of copying
// them when applying. This obviously requires that each of the mounts provided are for immutable, committed snapshots.
// externalHardlinks tracks any such hardlinks, which is needed for doing correct disk usage calculations elsewhere.
func diffApply(ctx context.Context, lowerMountable, upperMountable, applyMountable Mountable, useHardlink bool, externalHardlinks map[uint64]struct{}) error {
	if applyMountable == nil {
		return errors.New("invalid nil apply mounts")
	}

	var lowerMounts []mount.Mount
	if lowerMountable != nil {
		mounts, unmountLower, err := lowerMountable.Mount()
		if err != nil {
			return err
		}
		lowerMounts = mounts
		defer unmountLower()
	}

	var upperMounts []mount.Mount
	if upperMountable != nil {
		mounts, unmountUpper, err := upperMountable.Mount()
		if err != nil {
			return err
		}
		upperMounts = mounts
		defer unmountUpper()
	}

	applyMounts, unmountApply, err := applyMountable.Mount()
	if err != nil {
		return err
	}
	defer unmountApply()

	return withTempMount(ctx, lowerMounts, func(lowerView string) error {
		return withTempMount(ctx, upperMounts, func(upperView string) error {
			applyMounter := withTempMount
			if useHardlink {
				applyMounter = withRWDirMount
			}
			return applyMounter(ctx, applyMounts, func(applyRoot string) error {
				type pathTime struct {
					applyPath string
					atime     unix.Timespec
					mtime     unix.Timespec
				}

				// times holds the paths+times we visited and need to set
				var times []pathTime

				visited := make(map[string]struct{})
				inodes := make(map[uint64]string)

				diffCalculator := func(cf fs.ChangeFunc) error {
					return fs.Changes(ctx, lowerView, upperView, cf)
				}
				upperRoot := upperView

				// Use the optimized overlay diff if possible
				if useHardlink {
					if upperdir, err := overlay.GetUpperdir(lowerMounts, upperMounts); err == nil {
						upperRoot = upperdir
						diffCalculator = func(cf fs.ChangeFunc) error {
							return overlay.Changes(ctx, cf, upperRoot, upperView, lowerView)
						}
					}
				}

				var changeFunc fs.ChangeFunc
				changeFunc = func(kind fs.ChangeKind, changePath string, upperFi os.FileInfo, prevErr error) error {
					if prevErr != nil {
						return prevErr
					}

					applyPath := filepath.Join(applyRoot, changePath)
					upperPath := filepath.Join(upperRoot, changePath)

					visited[upperPath] = struct{}{}

					if kind == fs.ChangeKindUnmodified {
						return nil
					}

					// When using overlay, a delete is represented with a whiteout device, which we actually want to
					// hardlink or recreate here. In that case, upperFi is non-nil. Otherwise, upperFi is nil and
					// there's nothing to do besides remove the existing path, so unlink and return early.
					if kind == fs.ChangeKindDelete && upperFi == nil {
						if err := os.RemoveAll(applyPath); err != nil {
							return errors.Wrapf(err, "failed to remove path %s during apply", applyPath)
						}
						return nil
					}

					upperType := upperFi.Mode().Type()
					if upperType == os.ModeIrregular {
						return errors.Errorf("unhandled irregular mode file during merge at path %q", changePath)
					}

					upperStat, ok := upperFi.Sys().(*syscall.Stat_t)
					if !ok {
						return errors.Errorf("unhandled stat type for %+v", upperFi)
					}

					// Check to see if the parent dir needs to be created. This is needed when we are visiting a dirent
					// that changes but never visit its parent dir because the parent did not change in the diff
					upperParent := filepath.Dir(upperPath)
					if upperParent != upperRoot {
						if _, ok := visited[upperParent]; !ok {
							parentInfo, err := os.Lstat(upperParent)
							if err != nil {
								return errors.Wrap(err, "failed to stat parent path during apply")
							}
							if err := changeFunc(fs.ChangeKindAdd, filepath.Dir(changePath), parentInfo, nil); err != nil {
								return err
							}
						}
					}

					applyFi, err := os.Lstat(applyPath)
					if err != nil && !os.IsNotExist(err) {
						return errors.Wrapf(err, "failed to stat path during apply")
					}

					// if there is an existing file/dir at the applyPath, delete it unless both it and upper are dirs (in which case they get merged)
					if applyFi != nil && !(applyFi.IsDir() && upperFi.IsDir()) {
						if err := os.RemoveAll(applyPath); err != nil {
							return errors.Wrapf(err, "failed to remove path %s during apply", applyPath)
						}
						applyFi = nil
					}

					// hardlink fast-path
					if useHardlink {
						switch upperType {
						case os.ModeDir, os.ModeNamedPipe, os.ModeSocket:
							// Directories can't be hard-linked, so they just have to be recreated.
							// Named pipes and sockets can be hard-linked but is best to avoid as it could enable IPC by sharing merge inputs.
							break
						default:
							// TODO:(sipsma) consider handling EMLINK by falling back to copy
							if err := os.Link(upperPath, applyPath); err != nil {
								return errors.Wrapf(err, "failed to hardlink %q to %q during apply", upperPath, applyPath)
							}
							// mark this inode as one coming from a separate snapshot, needed for disk usage calculations elsewhere
							externalHardlinks[upperStat.Ino] = struct{}{}
							return nil
						}
					}

					switch upperType {
					case 0: // regular file
						if upperStat.Nlink > 1 {
							if linkedPath, ok := inodes[upperStat.Ino]; ok {
								if err := os.Link(linkedPath, applyPath); err != nil {
									return errors.Wrap(err, "failed to create hardlink during apply")
								}
								return nil // no other metadata updates needed when hardlinking
							}
							inodes[upperStat.Ino] = applyPath
						}
						if err := fs.CopyFile(applyPath, upperPath); err != nil {
							return errors.Wrapf(err, "failed to copy from %s to %s during apply", upperPath, applyPath)
						}
					case os.ModeDir:
						if applyFi == nil {
							// applyPath doesn't exist, make it a dir
							if err := os.Mkdir(applyPath, upperFi.Mode()); err != nil {
								return errors.Wrap(err, "failed to create applied dir")
							}
						}
					case os.ModeSymlink:
						if target, err := os.Readlink(upperPath); err != nil {
							return errors.Wrap(err, "failed to read symlink during apply")
						} else if err := os.Symlink(target, applyPath); err != nil {
							return errors.Wrap(err, "failed to create symlink during apply")
						}
					case os.ModeCharDevice, os.ModeDevice, os.ModeNamedPipe, os.ModeSocket:
						if err := unix.Mknod(applyPath, uint32(upperFi.Mode()), int(upperStat.Rdev)); err != nil {
							return errors.Wrap(err, "failed to create device during apply")
						}
					default:
						// should never be here, all types should be handled
						return errors.Errorf("unhandled file type %q during merge at path %q", upperType.String(), changePath)
					}

					xattrs, err := sysx.LListxattr(upperPath)
					if err != nil {
						return errors.Wrapf(err, "failed to list xattrs of upper path %s", upperPath)
					}
					for _, xattr := range xattrs {
						if isOpaqueXattr(xattr) {
							// Don't recreate opaque xattrs during merge. These should only be set when using overlay snapshotters,
							// in which case we are converting from the "opaque whiteout" format to the "explicit whiteout" format during
							// the merge (as taken care of by the overlay differ).
							continue
						}
						xattrVal, err := sysx.LGetxattr(upperPath, xattr)
						if err != nil {
							return errors.Wrapf(err, "failed to get xattr %s of upper path %s", xattr, upperPath)
						}
						if err := sysx.LSetxattr(applyPath, xattr, xattrVal, 0); err != nil {
							// This can often fail, so just log it: https://github.com/moby/buildkit/issues/1189
							bklog.G(ctx).Debugf("failed to set xattr %s of path %s during apply", xattr, applyPath)
						}
					}

					if err := os.Lchown(applyPath, int(upperStat.Uid), int(upperStat.Gid)); err != nil {
						return errors.Wrap(err, "failed to chown applied dir")
					}

					if upperType != os.ModeSymlink {
						if err := os.Chmod(applyPath, upperFi.Mode()); err != nil {
							return errors.Wrap(err, "failed to chmod applied dir")
						}
					}

					// save the times we should set on this path, to be applied at the end.
					times = append(times, pathTime{
						applyPath: applyPath,
						atime: unix.Timespec{
							Sec:  upperStat.Atim.Sec,
							Nsec: upperStat.Atim.Nsec,
						},
						mtime: unix.Timespec{
							Sec:  upperStat.Mtim.Sec,
							Nsec: upperStat.Mtim.Nsec,
						},
					})
					return nil
				}

				if err := diffCalculator(changeFunc); err != nil {
					return err
				}

				// Set times now that everything has been modified.
				for i := range times {
					ts := times[len(times)-1-i]
					if err := unix.UtimesNanoAt(unix.AT_FDCWD, ts.applyPath, []unix.Timespec{ts.atime, ts.mtime}, unix.AT_SYMLINK_NOFOLLOW); err != nil {
						return errors.Wrapf(err, "failed to update times of path %q", ts.applyPath)
					}
				}

				return nil
			})
		})
	})
}

// diskUsage calculates the disk space used by the provided mounts, similar to the normal containerd snapshotter disk usage
// calculations but with the extra ability to take into account hardlinks that were created between snapshots, ensuring that
// they don't get double counted.
func diskUsage(ctx context.Context, mountable Mountable, externalHardlinks map[uint64]struct{}) (snapshots.Usage, error) {
	mounts, unmount, err := mountable.Mount()
	if err != nil {
		return snapshots.Usage{}, err
	}
	defer unmount()

	inodes := make(map[uint64]struct{})
	var usage snapshots.Usage
	if err := withRWDirMount(ctx, mounts, func(root string) error {
		return filepath.WalkDir(root, func(path string, dirent gofs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := dirent.Info()
			if err != nil {
				return err
			}
			stat := info.Sys().(*syscall.Stat_t)
			if _, ok := inodes[stat.Ino]; ok {
				return nil
			}
			inodes[stat.Ino] = struct{}{}
			if _, ok := externalHardlinks[stat.Ino]; !ok {
				usage.Inodes++
				usage.Size += stat.Blocks * 512 // 512 is always block size, see "man 2 stat"
			}
			return nil
		})
	}); err != nil {
		return snapshots.Usage{}, err
	}
	return usage, nil
}

func isOpaqueXattr(s string) bool {
	for _, k := range []string{"trusted.overlay.opaque", "user.overlay.opaque"} {
		if s == k {
			return true
		}
	}
	return false
}

// needsUserXAttr checks whether overlay mounts should be provided the userxattr option. We can't use
// NeedsUserXAttr from the overlayutils package directly because we don't always have direct knowledge
// of the root of the snapshotter state (such as when using a remote snapshotter). Instead, we create
// a temporary new snapshot and test using its root, which works because single layer snapshots will
// use bind-mounts even when created by an overlay based snapshotter.
func needsUserXAttr(ctx context.Context, sn Snapshotter, lm leases.Manager) (bool, error) {
	key := identity.NewID()

	ctx, done, err := leaseutil.WithLease(ctx, lm, leaseutil.MakeTemporary)
	if err != nil {
		return false, errors.Wrap(err, "failed to create lease for checking user xattr")
	}
	defer done(context.TODO())

	err = sn.Prepare(ctx, key, "")
	if err != nil {
		return false, err
	}
	mntable, err := sn.Mounts(ctx, key)
	if err != nil {
		return false, err
	}
	mnts, unmount, err := mntable.Mount()
	if err != nil {
		return false, err
	}
	defer unmount()

	var userxattr bool
	if err := mount.WithTempMount(ctx, mnts, func(root string) error {
		var err error
		userxattr, err = overlayutils.NeedsUserXAttr(root)
		return err
	}); err != nil {
		return false, err
	}
	return userxattr, nil
}
