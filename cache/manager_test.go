package cache

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"

	"github.com/containerd/containerd/namespaces"
	"github.com/containerd/containerd/snapshot/naive"
	"github.com/moby/buildkit/cache/metadata"
	"github.com/moby/buildkit/snapshot"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestManager(t *testing.T) {
	ctx := namespaces.WithNamespace(context.Background(), "buildkit-test")

	tmpdir, err := ioutil.TempDir("", "cachemanager")
	require.NoError(t, err)
	defer os.RemoveAll(tmpdir)

	cm := getCacheManager(t, tmpdir)

	_, err = cm.Get(ctx, "foobar")
	require.Error(t, err)

	checkDiskUsage(t, ctx, cm, 0, 0)

	active, err := cm.New(ctx, nil)
	require.NoError(t, err)

	m, err := active.Mount(ctx, false)
	require.NoError(t, err)

	lm := snapshot.LocalMounter(m)
	target, err := lm.Mount()
	require.NoError(t, err)

	fi, err := os.Stat(target)
	require.NoError(t, err)
	require.Equal(t, fi.IsDir(), true)

	err = lm.Unmount()
	require.NoError(t, err)

	_, err = cm.GetMutable(ctx, active.ID())
	require.Error(t, err)
	require.Equal(t, errLocked, errors.Cause(err))

	checkDiskUsage(t, ctx, cm, 1, 0)

	snap, err := active.Commit(ctx)
	require.NoError(t, err)

	checkDiskUsage(t, ctx, cm, 1, 0)

	_, err = cm.GetMutable(ctx, active.ID())
	require.Error(t, err)
	require.Equal(t, errLocked, errors.Cause(err))

	err = snap.Release(ctx)
	require.NoError(t, err)

	checkDiskUsage(t, ctx, cm, 0, 1)

	active, err = cm.GetMutable(ctx, active.ID())
	require.NoError(t, err)

	checkDiskUsage(t, ctx, cm, 1, 0)

	snap, err = active.Commit(ctx)
	require.NoError(t, err)

	checkDiskUsage(t, ctx, cm, 1, 0)

	err = snap.Finalize(ctx)
	require.NoError(t, err)

	err = snap.Release(ctx)
	require.NoError(t, err)

	_, err = cm.GetMutable(ctx, active.ID())
	require.Error(t, err)
	require.Equal(t, errNotFound, errors.Cause(err))

	_, err = cm.GetMutable(ctx, snap.ID())
	require.Error(t, err)
	require.Equal(t, errInvalid, errors.Cause(err))

	snap, err = cm.Get(ctx, snap.ID())
	require.NoError(t, err)

	snap2, err := cm.Get(ctx, snap.ID())
	require.NoError(t, err)

	checkDiskUsage(t, ctx, cm, 1, 0)

	err = snap.Release(ctx)
	require.NoError(t, err)

	active2, err := cm.New(ctx, snap2)
	require.NoError(t, err)

	checkDiskUsage(t, ctx, cm, 2, 0)

	snap3, err := active2.Commit(ctx)
	require.NoError(t, err)

	err = snap2.Release(ctx)
	require.NoError(t, err)

	checkDiskUsage(t, ctx, cm, 2, 0)

	err = snap3.Release(ctx)
	require.NoError(t, err)

	checkDiskUsage(t, ctx, cm, 0, 2)

	err = cm.Close()
	require.NoError(t, err)
}

func TestLazyCommit(t *testing.T) {
	ctx := namespaces.WithNamespace(context.Background(), "buildkit-test")

	tmpdir, err := ioutil.TempDir("", "cachemanager")
	require.NoError(t, err)
	defer os.RemoveAll(tmpdir)

	cm := getCacheManager(t, tmpdir)

	active, err := cm.New(ctx, nil)
	require.NoError(t, err)

	// after commit mutable is locked
	snap, err := active.Commit(ctx)
	require.NoError(t, err)

	_, err = cm.GetMutable(ctx, active.ID())
	require.Error(t, err)
	require.Equal(t, errLocked, errors.Cause(err))

	// immutable refs still work
	snap2, err := cm.Get(ctx, snap.ID())
	require.NoError(t, err)
	require.Equal(t, snap.ID(), snap2.ID())

	err = snap.Release(ctx)
	require.NoError(t, err)

	err = snap2.Release(ctx)
	require.NoError(t, err)

	// immutable work after final release as well
	snap, err = cm.Get(ctx, snap.ID())
	require.NoError(t, err)
	require.Equal(t, snap.ID(), snap2.ID())
	err = snap.Release(ctx)
	require.NoError(t, err)

	// after release mutable becomes available again
	active2, err := cm.GetMutable(ctx, active.ID())
	require.NoError(t, err)
	require.Equal(t, active2.ID(), active.ID())

	// because ref was took mutable old immutable are cleared
	_, err = cm.Get(ctx, snap.ID())
	require.Error(t, err)
	require.Equal(t, errNotFound, errors.Cause(err))

	snap, err = active2.Commit(ctx)
	require.NoError(t, err)

	// this time finalize commit
	err = snap.Finalize(ctx)
	require.NoError(t, err)

	err = snap.Release(ctx)
	require.NoError(t, err)

	// mutable is gone after finalize
	_, err = cm.GetMutable(ctx, active2.ID())
	require.Error(t, err)
	require.Equal(t, errNotFound, errors.Cause(err))

	// immutable still works
	snap2, err = cm.Get(ctx, snap.ID())
	require.NoError(t, err)
	require.Equal(t, snap.ID(), snap2.ID())

	err = snap2.Release(ctx)
	require.NoError(t, err)

	// test restarting after commit
	active, err = cm.New(ctx, nil)
	require.NoError(t, err)

	// after commit mutable is locked
	snap, err = active.Commit(ctx)
	require.NoError(t, err)

	err = cm.Close()
	require.NoError(t, err)

	cm = getCacheManager(t, tmpdir)

	snap2, err = cm.Get(ctx, snap.ID())
	require.NoError(t, err)

	err = snap2.Release(ctx)
	require.NoError(t, err)

	active, err = cm.GetMutable(ctx, active.ID())
	require.NoError(t, err)

	_, err = cm.Get(ctx, snap.ID())
	require.Error(t, err)
	require.Equal(t, errNotFound, errors.Cause(err))

	snap, err = active.Commit(ctx)
	require.NoError(t, err)

	err = cm.Close()
	require.NoError(t, err)

	cm = getCacheManager(t, tmpdir)

	snap2, err = cm.Get(ctx, snap.ID())
	require.NoError(t, err)

	err = snap2.Finalize(ctx)
	require.NoError(t, err)

	err = snap2.Release(ctx)
	require.NoError(t, err)

	active, err = cm.GetMutable(ctx, active.ID())
	require.Error(t, err)
	require.Equal(t, errNotFound, errors.Cause(err))
}

func getCacheManager(t *testing.T, tmpdir string) Manager {
	snapshotter, err := naive.NewSnapshotter(filepath.Join(tmpdir, "snapshots"))
	require.NoError(t, err)

	md, err := metadata.NewStore(filepath.Join(tmpdir, "metadata.db"))
	require.NoError(t, err)

	cm, err := NewManager(ManagerOpt{
		Snapshotter:   snapshotter,
		MetadataStore: md,
	})
	require.NoError(t, err, fmt.Sprintf("error: %+v", err))
	return cm
}

func checkDiskUsage(t *testing.T, ctx context.Context, cm Manager, inuse, unused int) {
	du, err := cm.DiskUsage(ctx)
	require.NoError(t, err)
	var inuseActual, unusedActual int
	for _, r := range du {
		if r.InUse {
			inuseActual++
		} else {
			unusedActual++
		}
	}
	require.Equal(t, inuse, inuseActual)
	require.Equal(t, unused, unusedActual)
}
