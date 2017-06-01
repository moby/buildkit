// +build linux

package control

import (
	"context"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"

	"github.com/containerd/containerd/archive"
	"github.com/containerd/containerd/archive/compression"
	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/mount"
	"github.com/containerd/containerd/rootfs"
	cdsnapshot "github.com/containerd/containerd/snapshot"
	"github.com/containerd/containerd/snapshot/overlay"
	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/tonistiigi/buildkit_poc/cache"
	"github.com/tonistiigi/buildkit_poc/snapshot"
	"github.com/tonistiigi/buildkit_poc/snapshot/blobmapping"
	"github.com/tonistiigi/buildkit_poc/source"
	"github.com/tonistiigi/buildkit_poc/source/containerimage"
	"github.com/tonistiigi/buildkit_poc/worker"
	"github.com/tonistiigi/buildkit_poc/worker/runcworker"
)

func TestControl(t *testing.T) {
	// this should be an example or e2e test
	tmpdir, err := ioutil.TempDir("", "controltest")
	assert.NoError(t, err)
	defer os.RemoveAll(tmpdir)

	cd, err := localContainerd(tmpdir)
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

	snap, err := sm.Pull(context.TODO(), img)
	assert.NoError(t, err)

	mounts, err := snap.Mount()
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

	du, err := cm.DiskUsage(context.TODO())
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
		// Args: []string{"/bin/sleep", "3"},
		Cwd: "/",
	}

	m := make(map[string]cache.Mountable)
	m["/"] = snap

	err = w.Exec(context.TODO(), meta, m)
	assert.Error(t, err) // Read-only root

	root, err := cm.New(snap)
	assert.NoError(t, err)

	m["/"] = root
	err = w.Exec(context.TODO(), meta, m)
	assert.NoError(t, err)

	rf, err := root.Freeze()
	assert.NoError(t, err)

	mounts, err = rf.Mount()
	assert.NoError(t, err)

	lm = snapshot.LocalMounter(mounts)

	target, err = lm.Mount()
	assert.NoError(t, err)

	dt, err := ioutil.ReadFile(filepath.Join(target, "bar"))
	assert.NoError(t, err)
	assert.Equal(t, string(dt), "foo\n")

	lm.Unmount()
	assert.NoError(t, err)

	err = rf.Release()
	assert.NoError(t, err)

	err = snap.Release()
	assert.NoError(t, err)

	du2, err := cm.DiskUsage(context.TODO())
	assert.NoError(t, err)
	assert.Equal(t, 1, len(du2)-len(du))

}

type containerd struct {
	Snapshotter  cdsnapshot.Snapshotter
	ContentStore content.Store
	Applier      rootfs.Applier
}

func localContainerd(root string) (*containerd, error) {
	s, err := overlay.NewSnapshotter(filepath.Join(root, "snapshots"))
	if err != nil {
		return nil, err
	}

	c, err := content.NewStore(filepath.Join(root, "content"))
	if err != nil {
		return nil, err
	}

	a := &localApplier{root: root, content: c}

	return &containerd{
		Snapshotter:  s,
		ContentStore: c,
		Applier:      a,
	}, nil
}

// this should be exposed by containerd
type localApplier struct {
	root    string
	content content.Store
}

func (a *localApplier) Apply(ctx context.Context, desc ocispec.Descriptor, mounts []mount.Mount) (ocispec.Descriptor, error) {
	dir, err := ioutil.TempDir(a.root, "extract-")
	if err != nil {
		return ocispec.Descriptor{}, errors.Wrap(err, "failed to create temporary directory")
	}
	defer os.RemoveAll(dir)
	if err := mount.MountAll(mounts, dir); err != nil {
		return ocispec.Descriptor{}, errors.Wrap(err, "failed to mount")
	}
	defer mount.Unmount(dir, 0)

	r, err := a.content.Reader(ctx, desc.Digest)
	if err != nil {
		return ocispec.Descriptor{}, errors.Wrap(err, "failed to get reader from content store")
	}
	defer r.Close()

	// TODO: only decompress stream if media type is compressed
	ds, err := compression.DecompressStream(r)
	if err != nil {
		return ocispec.Descriptor{}, err
	}
	defer ds.Close()

	digester := digest.Canonical.Digester()
	rc := &readCounter{
		r: io.TeeReader(ds, digester.Hash()),
	}

	if _, err := archive.Apply(ctx, dir, rc); err != nil {
		return ocispec.Descriptor{}, err
	}

	// Read any trailing data
	if _, err := io.Copy(ioutil.Discard, rc); err != nil {
		return ocispec.Descriptor{}, err
	}

	return ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageLayer,
		Digest:    digester.Digest(),
		Size:      rc.c,
	}, nil
}

type readCounter struct {
	r io.Reader
	c int64
}

func (rc *readCounter) Read(p []byte) (n int, err error) {
	n, err = rc.r.Read(p)
	rc.c += int64(n)
	return
}
