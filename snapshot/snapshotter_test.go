//go:build linux
// +build linux

package snapshot

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/containerd/containerd/content/local"
	"github.com/containerd/containerd/leases"
	ctdmetadata "github.com/containerd/containerd/metadata"
	"github.com/containerd/containerd/mount"
	"github.com/containerd/containerd/namespaces"
	"github.com/containerd/containerd/snapshots"
	"github.com/containerd/containerd/snapshots/native"
	"github.com/containerd/containerd/snapshots/overlay"
	"github.com/containerd/continuity/fs/fstest"
	"github.com/hashicorp/go-multierror"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/util/leaseutil"
	"github.com/stretchr/testify/require"
	bolt "go.etcd.io/bbolt"
)

func newSnapshotter(ctx context.Context, snapshotterName string) (_ context.Context, _ *mergeSnapshotter, _ func() error, rerr error) {
	ns := "buildkit-test"
	ctx = namespaces.WithNamespace(ctx, ns)

	defers := make([]func() error, 0)
	cleanup := func() error {
		var err error
		for i := range defers {
			err = multierror.Append(err, defers[len(defers)-1-i]()).ErrorOrNil()
		}
		return err
	}
	defer func() {
		if rerr != nil && cleanup != nil {
			cleanup()
		}
	}()

	tmpdir, err := ioutil.TempDir("", "buildkit-test")
	if err != nil {
		return nil, nil, nil, err
	}
	defers = append(defers, func() error {
		return os.RemoveAll(tmpdir)
	})

	var ctdSnapshotter snapshots.Snapshotter
	var noHardlink bool
	switch snapshotterName {
	case "native-nohardlink":
		noHardlink = true
		fallthrough
	case "native":
		ctdSnapshotter, err = native.NewSnapshotter(filepath.Join(tmpdir, "snapshots"))
		if err != nil {
			return nil, nil, nil, err
		}
	case "overlayfs":
		ctdSnapshotter, err = overlay.NewSnapshotter(filepath.Join(tmpdir, "snapshots"))
		if err != nil {
			return nil, nil, nil, err
		}
	default:
		return nil, nil, nil, fmt.Errorf("unhandled snapshotter: %s", snapshotterName)
	}

	store, err := local.NewStore(tmpdir)
	if err != nil {
		return nil, nil, nil, err
	}

	db, err := bolt.Open(filepath.Join(tmpdir, "containerdmeta.db"), 0644, nil)
	if err != nil {
		return nil, nil, nil, err
	}
	defers = append(defers, func() error {
		return db.Close()
	})

	mdb := ctdmetadata.NewDB(db, store, map[string]snapshots.Snapshotter{
		snapshotterName: ctdSnapshotter,
	})
	if err := mdb.Init(context.TODO()); err != nil {
		return nil, nil, nil, err
	}

	lm := leaseutil.WithNamespace(ctdmetadata.NewLeaseManager(mdb), ns)
	snapshotter := NewMergeSnapshotter(ctx, FromContainerdSnapshotter(snapshotterName, mdb.Snapshotter(snapshotterName), nil), lm).(*mergeSnapshotter)
	if noHardlink {
		snapshotter.useHardlinks = false
	}

	leaseID := identity.NewID()
	_, err = lm.Create(ctx, func(l *leases.Lease) error {
		l.ID = leaseID
		l.Labels = map[string]string{
			"containerd.io/gc.flat": time.Now().UTC().Format(time.RFC3339Nano),
		}
		return nil
	}, leaseutil.MakeTemporary)
	if err != nil {
		return nil, nil, nil, err
	}
	ctx = leases.WithLease(ctx, leaseID)

	return ctx, snapshotter, cleanup, nil
}

func TestMerge(t *testing.T) {
	for _, snName := range []string{"overlayfs", "native", "native-nohardlink"} {
		snName := snName
		t.Run(snName, func(t *testing.T) {
			t.Parallel()
			if snName == "overlayfs" {
				requireRoot(t)
			}

			ctx, sn, cleanup, err := newSnapshotter(context.Background(), snName)
			require.NoError(t, err)
			defer cleanup()

			ts := time.Unix(0, 0)
			snapA := committedKey(ctx, t, sn, identity.NewID(), "",
				fstest.CreateFile("foo", []byte("A"), 0777),
				fstest.Lchtimes("foo", ts, ts.Add(2*time.Second)),

				fstest.CreateFile("a", []byte("A"), 0777),
				fstest.Lchtimes("a", ts, ts.Add(4*time.Second)),

				fstest.CreateDir("bar", 0700),
				fstest.CreateFile("bar/A", []byte("A"), 0777),
				fstest.Lchtimes("bar/A", ts, ts.Add(6*time.Second)),
				fstest.Lchtimes("bar", ts, ts.Add(6*time.Second)),
			)
			snapB := committedKey(ctx, t, sn, identity.NewID(), snapA.Name,
				fstest.Remove("/foo"),

				fstest.CreateFile("b", []byte("B"), 0777),
				fstest.Lchtimes("b", ts, ts.Add(4*time.Second)),

				fstest.CreateFile("bar/B", []byte("B"), 0774),
				fstest.Lchtimes("bar/B", ts, ts.Add(9*time.Second)),
				fstest.Lchtimes("bar", ts, ts.Add(9*time.Second)),
			)
			snapC := committedKey(ctx, t, sn, identity.NewID(), "",
				fstest.CreateFile("foo", []byte("C"), 0775),
				fstest.Lchtimes("foo", ts, ts.Add(4*time.Second)),

				fstest.CreateFile("c", []byte("C"), 0777),
				fstest.Lchtimes("c", ts, ts.Add(6*time.Second)),

				fstest.CreateDir("bar", 0777),
				fstest.CreateFile("bar/A", []byte("C"), 0400),
				fstest.Lchtimes("bar/A", ts, ts.Add(12*time.Second)),
				fstest.Lchtimes("bar", ts, ts.Add(12*time.Second)),

				fstest.Symlink("foo", "symlink"),
				fstest.Lchtimes("symlink", ts, ts.Add(3*time.Second)),
				fstest.Link("bar/A", "hardlink"),
				fstest.Symlink("../..", "dontfollowme"),
				fstest.Lchtimes("dontfollowme", ts, ts.Add(2*time.Second)),
			)

			mergeA := mergeKey(ctx, t, sn, identity.NewID(), []Diff{
				{"", snapA.Name}, {snapA.Name, snapB.Name},
				{"", snapC.Name},
			})
			requireContents(ctx, t, sn, mergeA.Name,
				fstest.CreateFile("a", []byte("A"), 0777),
				fstest.CreateFile("b", []byte("B"), 0777),
				fstest.CreateFile("c", []byte("C"), 0777),

				fstest.CreateFile("foo", []byte("C"), 0775),

				fstest.CreateDir("bar", 0777),
				fstest.CreateFile("bar/A", []byte("C"), 0400),
				fstest.CreateFile("bar/B", []byte("B"), 0774),

				fstest.Symlink("foo", "symlink"),
				fstest.Link("bar/A", "hardlink"),
				fstest.Symlink("../..", "dontfollowme"),
			)
			withMount(ctx, t, sn, mergeA.Name, func(root string) {
				requireMtime(t, filepath.Join(root, "a"), ts.Add(4*time.Second))
				requireMtime(t, filepath.Join(root, "b"), ts.Add(4*time.Second))
				requireMtime(t, filepath.Join(root, "c"), ts.Add(6*time.Second))
				requireMtime(t, filepath.Join(root, "foo"), ts.Add(4*time.Second))
				requireMtime(t, filepath.Join(root, "bar"), ts.Add(12*time.Second))
				requireMtime(t, filepath.Join(root, "bar/A"), ts.Add(12*time.Second))
				requireMtime(t, filepath.Join(root, "bar/B"), ts.Add(9*time.Second))
				requireMtime(t, filepath.Join(root, "symlink"), ts.Add(3*time.Second))
				requireMtime(t, filepath.Join(root, "dontfollowme"), ts.Add(2*time.Second))
			})

			mergeB := mergeKey(ctx, t, sn, identity.NewID(), []Diff{
				{"", snapC.Name},
				{"", snapA.Name}, {snapA.Name, snapB.Name},
			})
			requireContents(ctx, t, sn, mergeB.Name,
				fstest.CreateFile("a", []byte("A"), 0777),
				fstest.CreateFile("b", []byte("B"), 0777),
				fstest.CreateFile("c", []byte("C"), 0777),

				fstest.CreateDir("bar", 0700),
				fstest.CreateFile("bar/A", []byte("A"), 0777),
				fstest.CreateFile("bar/B", []byte("B"), 0774),

				fstest.Symlink("foo", "symlink"),
				fstest.CreateFile("hardlink", []byte("C"), 0400), // bar/A was overwritten, not considered hardlink
				fstest.Symlink("../..", "dontfollowme"),
			)
			withMount(ctx, t, sn, mergeB.Name, func(root string) {
				requireMtime(t, filepath.Join(root, "a"), ts.Add(4*time.Second))
				requireMtime(t, filepath.Join(root, "b"), ts.Add(4*time.Second))
				requireMtime(t, filepath.Join(root, "c"), ts.Add(6*time.Second))
				requireMtime(t, filepath.Join(root, "bar"), ts.Add(9*time.Second))
				requireMtime(t, filepath.Join(root, "bar/A"), ts.Add(6*time.Second))
				requireMtime(t, filepath.Join(root, "bar/B"), ts.Add(9*time.Second))
				requireMtime(t, filepath.Join(root, "symlink"), ts.Add(3*time.Second))
				requireMtime(t, filepath.Join(root, "dontfollowme"), ts.Add(2*time.Second))
			})

			snapD := committedKey(ctx, t, sn, identity.NewID(), "",
				fstest.CreateDir("bar", 0750),
				fstest.CreateFile("bar/D", []byte("D"), 0444),
				fstest.CreateDir("fs", 0770),
				fstest.CreateFile("x", []byte("X"), 0400),
				fstest.Link("x", "hardlink"),
				fstest.Symlink("fs", "symlink"),
				fstest.Link("symlink", "hardsymlink"),
			)

			mergeC := mergeKey(ctx, t, sn, identity.NewID(), []Diff{
				// mergeA
				{"", snapA.Name}, {snapA.Name, snapB.Name},
				{"", snapC.Name},
				// mergeB
				{"", snapC.Name},
				{"", snapA.Name}, {snapA.Name, snapB.Name},
				// snapD
				{"", snapD.Name},
			})
			requireContents(ctx, t, sn, mergeC.Name,
				fstest.CreateFile("a", []byte("A"), 0777),
				fstest.CreateFile("b", []byte("B"), 0777),
				fstest.CreateFile("c", []byte("C"), 0777),
				fstest.CreateDir("bar", 0750),
				fstest.CreateFile("bar/A", []byte("A"), 0777),
				fstest.CreateFile("bar/B", []byte("B"), 0774),
				fstest.CreateFile("bar/D", []byte("D"), 0444),
				fstest.CreateDir("fs", 0770),
				fstest.CreateFile("x", []byte("X"), 0400),
				fstest.Link("x", "hardlink"),
				fstest.Symlink("fs", "symlink"),
				fstest.Link("symlink", "hardsymlink"),
				fstest.Symlink("../..", "dontfollowme"),
			)

			snapE := committedKey(ctx, t, sn, identity.NewID(), "",
				fstest.CreateFile("qaz", nil, 0444),
				fstest.CreateDir("bar", 0777),
				fstest.CreateFile("bar/B", []byte("B"), 0444),
			)
			snapF := committedKey(ctx, t, sn, identity.NewID(), mergeC.Name,
				fstest.Remove("a"),
				fstest.CreateDir("a", 0770),
				fstest.Rename("b", "a/b"),
				fstest.Rename("c", "a/c"),

				fstest.RemoveAll("bar"),
				fstest.CreateDir("bar", 0777),
				fstest.CreateFile("bar/D", []byte("D2"), 0444),

				fstest.RemoveAll("fs"),
				fstest.CreateFile("fs", nil, 0764),

				fstest.Remove("x"),
				fstest.CreateDir("x", 0555),

				fstest.Remove("hardsymlink"),
				fstest.CreateDir("hardsymlink", 0707),
			)

			mergeD := mergeKey(ctx, t, sn, identity.NewID(), []Diff{
				{"", snapE.Name},
				// mergeC
				{"", snapA.Name}, {snapA.Name, snapB.Name},
				{"", snapC.Name},
				{"", snapC.Name},
				{"", snapA.Name}, {snapA.Name, snapB.Name},
				{"", snapD.Name},
				// snapF
				{mergeC.Name, snapF.Name},
			})
			requireContents(ctx, t, sn, mergeD.Name,
				fstest.CreateDir("a", 0770),
				fstest.CreateFile("a/b", []byte("B"), 0777),
				fstest.CreateDir("bar", 0777),
				fstest.CreateFile("a/c", []byte("C"), 0777),
				fstest.CreateFile("bar/D", []byte("D2"), 0444),
				fstest.CreateFile("fs", nil, 0764),
				fstest.CreateDir("x", 0555),
				fstest.CreateFile("hardlink", []byte("X"), 0400),
				fstest.Symlink("fs", "symlink"),
				fstest.CreateDir("hardsymlink", 0707),
				fstest.Symlink("../..", "dontfollowme"),
				fstest.CreateFile("qaz", nil, 0444),
			)
		})
	}
}

func TestHardlinks(t *testing.T) {
	for _, snName := range []string{"overlayfs", "native"} {
		snName := snName
		t.Run(snName, func(t *testing.T) {
			t.Parallel()
			if snName == "overlayfs" {
				requireRoot(t)
			}

			ctx, sn, cleanup, err := newSnapshotter(context.Background(), snName)
			require.NoError(t, err)
			defer cleanup()

			base1Snap := committedKey(ctx, t, sn, identity.NewID(), "",
				fstest.CreateFile("1", []byte("1"), 0600),
			)
			base2Snap := committedKey(ctx, t, sn, identity.NewID(), "",
				fstest.CreateFile("2", []byte("2"), 0600),
			)

			mergeSnap := mergeKey(ctx, t, sn, identity.NewID(), []Diff{
				{"", base1Snap.Name},
				{"", base2Snap.Name},
			})
			stat1 := statPath(ctx, t, sn, mergeSnap.Name, "1")
			var expected1Links int
			switch snName {
			case "overlayfs":
				// base merge input is used as parent, not merged with hardlinks
				expected1Links = 1
			case "native":
				expected1Links = 2
			}
			require.EqualValues(t, expected1Links, stat1.Nlink)
			stat1Ino := stat1.Ino

			stat2 := statPath(ctx, t, sn, mergeSnap.Name, "2")
			require.EqualValues(t, 2, stat2.Nlink)
			stat2Ino := stat2.Ino

			childSnap := committedKey(ctx, t, sn, identity.NewID(), mergeSnap.Name,
				fstest.CreateFile("1", []byte("11"), 0644),
				fstest.CreateFile("2", []byte("22"), 0644),
			)
			stat1 = statPath(ctx, t, sn, childSnap.Name, "1")
			require.EqualValues(t, 1, stat1.Nlink)
			require.NotEqualValues(t, stat1Ino, stat1.Ino)
			stat2 = statPath(ctx, t, sn, childSnap.Name, "2")
			require.EqualValues(t, 1, stat2.Nlink)
			require.NotEqualValues(t, stat2Ino, stat2.Ino)

			// verify the original files and the files inthe merge are unchanged
			requireContents(ctx, t, sn, base1Snap.Name,
				fstest.CreateFile("1", []byte("1"), 0600),
			)
			requireContents(ctx, t, sn, base2Snap.Name,
				fstest.CreateFile("2", []byte("2"), 0600),
			)
			requireContents(ctx, t, sn, mergeSnap.Name,
				fstest.CreateFile("1", []byte("1"), 0600),
				fstest.CreateFile("2", []byte("2"), 0600),
			)
		})
	}
}

func TestUsage(t *testing.T) {
	for _, snName := range []string{"overlayfs", "native", "native-nohardlink"} {
		snName := snName
		t.Run(snName, func(t *testing.T) {
			t.Parallel()
			if snName == "overlayfs" {
				requireRoot(t)
			}

			ctx, sn, cleanup, err := newSnapshotter(context.Background(), snName)
			require.NoError(t, err)
			defer cleanup()

			const direntByteSize = 4096

			base1Snap := committedKey(ctx, t, sn, identity.NewID(), "",
				fstest.CreateDir("foo", 0777),
				fstest.CreateFile("foo/1", []byte("a"), 0777),
			)
			require.EqualValues(t, 3, base1Snap.Inodes)
			require.EqualValues(t, 3*direntByteSize, base1Snap.Size)

			base2Snap := committedKey(ctx, t, sn, identity.NewID(), "",
				fstest.CreateDir("foo", 0777),
				fstest.CreateFile("foo/2", []byte("aa"), 0777),
			)
			require.EqualValues(t, 3, base2Snap.Inodes)
			require.EqualValues(t, 3*direntByteSize, base2Snap.Size)

			base3Snap := committedKey(ctx, t, sn, identity.NewID(), "",
				fstest.CreateDir("foo", 0777),
				fstest.CreateFile("foo/3", []byte("aaa"), 0777),
				fstest.CreateFile("bar", nil, 0777),
			)
			require.EqualValues(t, 4, base3Snap.Inodes)
			require.EqualValues(t, 3*direntByteSize, base3Snap.Size)

			mergeSnap := mergeKey(ctx, t, sn, identity.NewID(), []Diff{
				{"", base1Snap.Name},
				{"", base2Snap.Name},
				{"", base3Snap.Name},
			})
			switch snName {
			case "overlayfs", "native":
				// / and /foo were created/copied. Others should be hard-linked
				require.EqualValues(t, 2, mergeSnap.Inodes)
				require.EqualValues(t, 2*direntByteSize, mergeSnap.Size)
			case "native-nohardlink":
				require.EqualValues(t, 6, mergeSnap.Inodes)
				require.EqualValues(t, 5*direntByteSize, mergeSnap.Size)
			}
		})
	}
}

type snapshotInfo struct {
	snapshots.Info
	snapshots.Usage
}

func getInfo(ctx context.Context, t *testing.T, sn *mergeSnapshotter, key string) snapshotInfo {
	t.Helper()
	info, err := sn.Stat(ctx, key)
	require.NoError(t, err)
	usage, err := sn.Usage(ctx, key)
	require.NoError(t, err)
	return snapshotInfo{info, usage}
}

func activeKey(
	ctx context.Context,
	t *testing.T,
	sn *mergeSnapshotter,
	key string,
	parent string,
	files ...fstest.Applier,
) snapshotInfo {
	t.Helper()

	err := sn.Prepare(ctx, key, parent)
	require.NoError(t, err)

	if len(files) > 0 {
		mnts, cleanup := getMounts(ctx, t, sn, key)
		defer cleanup()
		mnter := LocalMounterWithMounts(mnts)
		root, err := mnter.Mount()
		require.NoError(t, err)
		defer mnter.Unmount()
		require.NoError(t, fstest.Apply(files...).Apply(root))
	}

	return getInfo(ctx, t, sn, key)
}

func commitActiveKey(
	ctx context.Context,
	t *testing.T,
	sn *mergeSnapshotter,
	name string,
	activeKey string,
) snapshotInfo {
	t.Helper()
	err := sn.Commit(ctx, name, activeKey)
	require.NoError(t, err)
	return getInfo(ctx, t, sn, name)
}

func committedKey(
	ctx context.Context,
	t *testing.T,
	sn *mergeSnapshotter,
	key string,
	parent string,
	files ...fstest.Applier,
) snapshotInfo {
	t.Helper()
	prepareKey := identity.NewID()
	activeKey(ctx, t, sn, prepareKey, parent, files...)
	return commitActiveKey(ctx, t, sn, key, prepareKey)
}

func mergeKey(
	ctx context.Context,
	t *testing.T,
	sn *mergeSnapshotter,
	key string,
	diffs []Diff,
) snapshotInfo {
	t.Helper()
	err := sn.Merge(ctx, key, diffs)
	require.NoError(t, err)
	return getInfo(ctx, t, sn, key)
}

func getMounts(ctx context.Context, t *testing.T, sn *mergeSnapshotter, key string) ([]mount.Mount, func() error) {
	t.Helper()

	var mntable Mountable
	var err error
	if info := getInfo(ctx, t, sn, key); info.Kind == snapshots.KindCommitted {
		mntable, err = sn.View(ctx, identity.NewID(), key)
	} else {
		mntable, err = sn.Mounts(ctx, key)
	}
	require.NoError(t, err)

	mnts, cleanup, err := mntable.Mount()
	require.NoError(t, err)
	return mnts, cleanup
}

func withMount(ctx context.Context, t *testing.T, sn *mergeSnapshotter, key string, f func(root string)) {
	t.Helper()
	mounts, cleanup := getMounts(ctx, t, sn, key)
	defer cleanup()
	mnter := LocalMounterWithMounts(mounts)
	root, err := mnter.Mount()
	require.NoError(t, err)
	defer mnter.Unmount()
	f(root)
}

func requireContents(ctx context.Context, t *testing.T, sn *mergeSnapshotter, key string, files ...fstest.Applier) {
	t.Helper()
	withMount(ctx, t, sn, key, func(root string) {
		require.NoError(t, fstest.CheckDirectoryEqualWithApplier(root, fstest.Apply(files...)))
	})
}

func trySyscallStat(t *testing.T, path string) *syscall.Stat_t {
	t.Helper()
	info, err := os.Stat(path)
	if err == nil {
		return info.Sys().(*syscall.Stat_t)
	}
	require.ErrorIs(t, err, os.ErrNotExist)
	return nil
}

func requireMtime(t *testing.T, path string, mtime time.Time) {
	t.Helper()
	info, err := os.Lstat(path)
	require.NoError(t, err)
	stat := info.Sys().(*syscall.Stat_t)
	require.Equal(t, mtime.UnixNano(), stat.Mtim.Nano())
}

func tryStatPath(ctx context.Context, t *testing.T, sn *mergeSnapshotter, key, path string) (st *syscall.Stat_t) {
	t.Helper()
	mounts, cleanup := getMounts(ctx, t, sn, key)
	defer cleanup()
	require.Len(t, mounts, 1)
	mnt := mounts[0]

	if mnt.Type == "overlay" {
		var upperdir string
		var lowerdirs []string
		for _, opt := range mnt.Options {
			if strings.HasPrefix(opt, "upperdir=") {
				upperdir = strings.SplitN(opt, "upperdir=", 2)[1]
			} else if strings.HasPrefix(opt, "lowerdir=") {
				lowerdirs = strings.Split(strings.SplitN(opt, "lowerdir=", 2)[1], ":")
			}
		}
		if upperdir != "" {
			st = trySyscallStat(t, filepath.Join(upperdir, path))
			if st != nil {
				return st
			}
		}
		for _, lowerdir := range lowerdirs {
			st = trySyscallStat(t, filepath.Join(lowerdir, path))
			if st != nil {
				return st
			}
		}
		return nil
	}

	withMount(ctx, t, sn, key, func(root string) {
		st = trySyscallStat(t, filepath.Join(root, path))
	})
	return st
}

func statPath(ctx context.Context, t *testing.T, sn *mergeSnapshotter, key, path string) (st *syscall.Stat_t) {
	t.Helper()
	st = tryStatPath(ctx, t, sn, key, path)
	require.NotNil(t, st)
	return st
}

func requireRoot(t *testing.T) {
	t.Helper()
	if os.Getuid() != 0 {
		t.Skip("test requires root")
	}
}
