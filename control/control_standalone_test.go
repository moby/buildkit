// +build linux standalone

package control

import (
	"bytes"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"

	"github.com/containerd/containerd/namespaces"
	"github.com/stretchr/testify/assert"
	"github.com/tonistiigi/buildkit_poc/cache"
	"github.com/tonistiigi/buildkit_poc/snapshot"
	"github.com/tonistiigi/buildkit_poc/snapshot/blobmapping"
	"github.com/tonistiigi/buildkit_poc/source"
	"github.com/tonistiigi/buildkit_poc/source/containerimage"
	"github.com/tonistiigi/buildkit_poc/worker"
	"github.com/tonistiigi/buildkit_poc/worker/runcworker"
	"golang.org/x/net/context"
)

func TestControl(t *testing.T) {
	ctx := namespaces.WithNamespace(context.Background(), "buildkit-test")

	// this should be an example or e2e test
	tmpdir, err := ioutil.TempDir("", "controltest")
	assert.NoError(t, err)
	defer os.RemoveAll(tmpdir)

	cd, err := newStandalonePullDeps(tmpdir)
	assert.NoError(t, err)

	snapshotter, err := blobmapping.NewSnapshotter(blobmapping.Opt{
		Root:        filepath.Join(tmpdir, "blobmap"),
		Content:     cd.ContentStore,
		Snapshotter: cd.Snapshotter,
	})
	assert.NoError(t, err)

	cm, err := cache.NewManager(cache.ManagerOpt{
		Snapshotter: snapshotter,
		Root:        filepath.Join(tmpdir, "cachemanager"),
	})
	assert.NoError(t, err)

	sm, err := source.NewManager()
	assert.NoError(t, err)

	is, err := containerimage.NewSource(containerimage.SourceOpt{
		Snapshotter:   snapshotter,
		ContentStore:  cd.ContentStore,
		Applier:       cd.Applier,
		CacheAccessor: cm,
	})
	assert.NoError(t, err)

	sm.Register(is)

	img, err := source.NewImageIdentifier("docker.io/library/busybox:latest")
	assert.NoError(t, err)

	snap, err := sm.Pull(ctx, img)
	assert.NoError(t, err)

	mounts, err := snap.Mount(ctx)
	assert.NoError(t, err)

	lm := snapshot.LocalMounter(mounts)

	target, err := lm.Mount()
	assert.NoError(t, err)

	f, err := os.Open(target)
	assert.NoError(t, err)

	names, err := f.Readdirnames(-1)
	assert.NoError(t, err)
	assert.True(t, len(names) > 5)

	err = f.Close()
	assert.NoError(t, err)

	lm.Unmount()
	assert.NoError(t, err)

	du, err := cm.DiskUsage(ctx)
	assert.NoError(t, err)

	// for _, d := range du {
	// 	fmt.Printf("du: %+v\n", d)
	// }

	for _, d := range du {
		assert.True(t, d.Size >= 8192)
	}

	w, err := runcworker.New(tmpdir)
	assert.NoError(t, err)

	meta := worker.Meta{
		Args: []string{"/bin/sh", "-c", "echo \"foo\" > /bar"},
		Cwd:  "/",
	}

	m := make(map[string]cache.Mountable)
	m["/"] = snap

	stderr := bytes.NewBuffer(nil)

	err = w.Exec(ctx, meta, m, nil, &nopCloser{stderr})
	assert.Error(t, err) // Read-only root
	assert.Contains(t, stderr.String(), "Read-only file system")

	root, err := cm.New(ctx, snap)
	assert.NoError(t, err)

	m["/"] = root
	err = w.Exec(ctx, meta, m, nil, nil)
	assert.NoError(t, err)

	rf, err := root.Freeze()
	assert.NoError(t, err)

	mounts, err = rf.Mount(ctx)
	assert.NoError(t, err)

	lm = snapshot.LocalMounter(mounts)

	target, err = lm.Mount()
	assert.NoError(t, err)

	dt, err := ioutil.ReadFile(filepath.Join(target, "bar"))
	assert.NoError(t, err)
	assert.Equal(t, string(dt), "foo\n")

	lm.Unmount()
	assert.NoError(t, err)

	err = rf.Release(ctx)
	assert.NoError(t, err)

	err = snap.Release(ctx)
	assert.NoError(t, err)

	du2, err := cm.DiskUsage(ctx)
	assert.NoError(t, err)
	assert.Equal(t, 1, len(du2)-len(du))

}
