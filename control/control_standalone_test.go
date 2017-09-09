// +build linux,standalone

package control

import (
	"bytes"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"

	"github.com/containerd/containerd/namespaces"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/cache/metadata"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/snapshot"
	"github.com/moby/buildkit/snapshot/blobmapping"
	"github.com/moby/buildkit/source"
	"github.com/moby/buildkit/source/containerimage"
	"github.com/moby/buildkit/worker"
	"github.com/moby/buildkit/worker/runcworker"
	"github.com/stretchr/testify/assert"
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

	md, err := metadata.NewStore(filepath.Join(tmpdir, "metadata.db"))
	assert.NoError(t, err)

	snapshotter, err := blobmapping.NewSnapshotter(blobmapping.Opt{
		Content:       cd.ContentStore,
		Snapshotter:   cd.Snapshotter,
		MetadataStore: md,
	})
	assert.NoError(t, err)

	cm, err := cache.NewManager(cache.ManagerOpt{
		Snapshotter:   snapshotter,
		MetadataStore: md,
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

	src, err := sm.Resolve(ctx, img)
	assert.NoError(t, err)

	snap, err := src.Snapshot(ctx)
	assert.NoError(t, err)

	mounts, err := snap.Mount(ctx, false)
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

	du, err := cm.DiskUsage(ctx, client.DiskUsageInfo{})
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

	stderr := bytes.NewBuffer(nil)

	err = w.Exec(ctx, meta, snap, nil, nil, &nopCloser{stderr})
	assert.Error(t, err) // Read-only root
	// typical error is like `mkdir /.../rootfs/proc: read-only file system`.
	// make sure the error is caused before running `echo foo > /bar`.
	assert.Contains(t, stderr.String(), "read-only file system")

	root, err := cm.New(ctx, snap)
	assert.NoError(t, err)

	err = w.Exec(ctx, meta, root, nil, nil, nil)
	assert.NoError(t, err)

	rf, err := root.Commit(ctx)
	assert.NoError(t, err)

	mounts, err = rf.Mount(ctx, false)
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

	du2, err := cm.DiskUsage(ctx, client.DiskUsageInfo{})
	assert.NoError(t, err)
	assert.Equal(t, 1, len(du2)-len(du))

}

type nopCloser struct {
	io.Writer
}

func (n *nopCloser) Close() error {
	return nil
}
