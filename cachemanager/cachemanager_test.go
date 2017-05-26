package cachemanager

import (
	"context"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"

	"github.com/containerd/containerd/snapshot/naive"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/tonistiigi/buildkit_poc/snapshot"
)

func TestCacheManager(t *testing.T) {
	tmpdir, err := ioutil.TempDir("", "cachemanager")
	assert.NoError(t, err)
	defer os.RemoveAll(tmpdir)

	snapshotter, err := naive.NewSnapshotter(filepath.Join(tmpdir, "snapshots"))
	assert.NoError(t, err)

	cm, err := NewCacheManager(CacheManagerOpt{
		Root:        tmpdir,
		Snapshotter: snapshotter,
	})
	assert.NoError(t, err)

	_, err = cm.Get("foobar")
	assert.Error(t, err)

	checkDiskUsage(t, cm, 0, 0)

	active, err := cm.New(nil)
	assert.NoError(t, err)

	m, err := active.Mount()
	assert.NoError(t, err)

	lm := snapshot.LocalMounter(m)
	target, err := lm.Mount()
	assert.NoError(t, err)

	fi, err := os.Stat(target)
	assert.NoError(t, err)
	assert.Equal(t, fi.IsDir(), true)

	err = lm.Unmount()
	assert.NoError(t, err)

	_, err = cm.GetActive(active.ID())
	assert.Error(t, err)
	assert.Equal(t, errLocked, errors.Cause(err))

	checkDiskUsage(t, cm, 1, 0)

	snap, err := active.ReleaseActive()
	assert.NoError(t, err)

	checkDiskUsage(t, cm, 1, 0)

	_, err = cm.GetActive(active.ID())
	assert.Error(t, err)
	assert.Equal(t, errLocked, errors.Cause(err))

	err = snap.Release()
	assert.NoError(t, err)

	checkDiskUsage(t, cm, 0, 1)

	active, err = cm.GetActive(active.ID())
	assert.NoError(t, err)

	checkDiskUsage(t, cm, 1, 0)

	snap, err = active.ReleaseAndCommit(context.TODO())
	assert.NoError(t, err)

	checkDiskUsage(t, cm, 1, 0)

	err = snap.Release()
	assert.NoError(t, err)

	_, err = cm.GetActive(active.ID())
	assert.Error(t, err)
	assert.Equal(t, errNotFound, errors.Cause(err))

	_, err = cm.GetActive(snap.ID())
	assert.Error(t, err)
	assert.Equal(t, errInvalid, errors.Cause(err))

	snap, err = cm.Get(snap.ID())
	assert.NoError(t, err)

	snap2, err := cm.Get(snap.ID())
	assert.NoError(t, err)

	checkDiskUsage(t, cm, 1, 0)

	err = snap.Release()
	assert.NoError(t, err)

	active2, err := cm.New(snap2)
	assert.NoError(t, err)

	checkDiskUsage(t, cm, 2, 0)

	snap3, err := active2.ReleaseActive()
	assert.NoError(t, err)

	err = snap2.Release()
	assert.NoError(t, err)

	checkDiskUsage(t, cm, 2, 0)

	err = snap3.Release()
	assert.NoError(t, err)

	checkDiskUsage(t, cm, 0, 2)

	err = cm.Close()
	assert.NoError(t, err)
}

func checkDiskUsage(t *testing.T, cm CacheManager, inuse, unused int) {
	du, err := cm.DiskUsage(context.TODO())
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
