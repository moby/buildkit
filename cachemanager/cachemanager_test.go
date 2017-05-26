package cachemanager

import (
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
	assert.Equal(t, errors.Cause(err), errLocked)

	snap, err := active.ReleaseActive()
	assert.NoError(t, err)

	_, err = cm.GetActive(active.ID())
	assert.Error(t, err)
	assert.Equal(t, errors.Cause(err), errLocked)

	err = snap.Release()
	assert.NoError(t, err)

	_, err = cm.GetActive(active.ID())
	assert.NoError(t, err)

	err = cm.Close()
	assert.NoError(t, err)
}
