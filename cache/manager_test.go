package cache

import (
	"context"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"

	"github.com/containerd/containerd/namespaces"
	"github.com/containerd/containerd/snapshot/naive"
	"github.com/moby/buildkit/cache/metadata"
	"github.com/moby/buildkit/snapshot"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
)

func TestManager(t *testing.T) {
	ctx := namespaces.WithNamespace(context.Background(), "buildkit-test")

	tmpdir, err := ioutil.TempDir("", "cachemanager")
	assert.NoError(t, err)
	defer os.RemoveAll(tmpdir)

	snapshotter, err := naive.NewSnapshotter(filepath.Join(tmpdir, "snapshots"))
	assert.NoError(t, err)

	md, err := metadata.NewStore(filepath.Join(tmpdir, "metadata.db"))
	assert.NoError(t, err)

	cm, err := NewManager(ManagerOpt{
		Snapshotter:   snapshotter,
		MetadataStore: md,
	})
	assert.NoError(t, err)

	_, err = cm.Get(ctx, "foobar")
	assert.Error(t, err)

	checkDiskUsage(t, ctx, cm, 0, 0)

	active, err := cm.New(ctx, nil)
	assert.NoError(t, err)

	m, err := active.Mount(ctx)
	assert.NoError(t, err)

	lm := snapshot.LocalMounter(m)
	target, err := lm.Mount()
	assert.NoError(t, err)

	fi, err := os.Stat(target)
	assert.NoError(t, err)
	assert.Equal(t, fi.IsDir(), true)

	err = lm.Unmount()
	assert.NoError(t, err)

	_, err = cm.GetMutable(ctx, active.ID())
	assert.Error(t, err)
	assert.Equal(t, errLocked, errors.Cause(err))

	checkDiskUsage(t, ctx, cm, 1, 0)

	snap, err := active.Freeze()
	assert.NoError(t, err)

	checkDiskUsage(t, ctx, cm, 1, 0)

	_, err = cm.GetMutable(ctx, active.ID())
	assert.Error(t, err)
	assert.Equal(t, errLocked, errors.Cause(err))

	err = snap.Release(ctx)
	assert.NoError(t, err)

	checkDiskUsage(t, ctx, cm, 0, 1)

	active, err = cm.GetMutable(ctx, active.ID())
	assert.NoError(t, err)

	checkDiskUsage(t, ctx, cm, 1, 0)

	snap, err = active.ReleaseAndCommit(ctx)
	assert.NoError(t, err)

	checkDiskUsage(t, ctx, cm, 1, 0)

	err = snap.Release(ctx)
	assert.NoError(t, err)

	_, err = cm.GetMutable(ctx, active.ID())
	assert.Error(t, err)
	assert.Equal(t, errNotFound, errors.Cause(err))

	_, err = cm.GetMutable(ctx, snap.ID())
	assert.Error(t, err)
	assert.Equal(t, errInvalid, errors.Cause(err))

	snap, err = cm.Get(ctx, snap.ID())
	assert.NoError(t, err)

	snap2, err := cm.Get(ctx, snap.ID())
	assert.NoError(t, err)

	checkDiskUsage(t, ctx, cm, 1, 0)

	err = snap.Release(ctx)
	assert.NoError(t, err)

	active2, err := cm.New(ctx, snap2)
	assert.NoError(t, err)

	checkDiskUsage(t, ctx, cm, 2, 0)

	snap3, err := active2.Freeze()
	assert.NoError(t, err)

	err = snap2.Release(ctx)
	assert.NoError(t, err)

	checkDiskUsage(t, ctx, cm, 2, 0)

	err = snap3.Release(ctx)
	assert.NoError(t, err)

	checkDiskUsage(t, ctx, cm, 0, 2)

	err = cm.Close()
	assert.NoError(t, err)
}

func checkDiskUsage(t *testing.T, ctx context.Context, cm Manager, inuse, unused int) {
	du, err := cm.DiskUsage(ctx)
	assert.NoError(t, err)
	var inuseActual, unusedActual int
	for _, r := range du {
		if r.InUse {
			inuseActual++
		} else {
			unusedActual++
		}
	}
	assert.Equal(t, inuse, inuseActual)
	assert.Equal(t, unused, unusedActual)
}
