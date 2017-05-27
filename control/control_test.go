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
	"github.com/containerd/containerd/snapshot"
	"github.com/containerd/containerd/snapshot/naive"
	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/tonistiigi/buildkit_poc/cachemanager"
	"github.com/tonistiigi/buildkit_poc/sources"
	"github.com/tonistiigi/buildkit_poc/sources/containerimage"
	"github.com/tonistiigi/buildkit_poc/sources/identifier"
)

func TestControl(t *testing.T) {
	tmpdir, err := ioutil.TempDir("", "controltest")
	assert.NoError(t, err)
	// defer os.RemoveAll(tmpdir)

	cd, err := localContainerd(tmpdir)
	assert.NoError(t, err)

	cm, err := cachemanager.NewCacheManager(cachemanager.CacheManagerOpt{
		Snapshotter: cd.Snapshotter,
		Root:        filepath.Join(tmpdir, "cachemanager"),
	})

	sm, err := sources.NewSourceManager()
	assert.NoError(t, err)

	is, err := containerimage.NewContainerImageSource(containerimage.ContainerImageSourceOpt{
		Snapshotter:   cd.Snapshotter,
		ContentStore:  cd.ContentStore,
		Applier:       cd.Applier,
		CacheAccessor: cm,
	})
	assert.NoError(t, err)

	sm.Register(is)

	img, err := identifier.NewImageIdentifier("docker.io/library/redis:latest")
	assert.NoError(t, err)

	snap, err := sm.Pull(context.TODO(), img)
	assert.NoError(t, err)

	_ = snap
}

type containerd struct {
	Snapshotter  snapshot.Snapshotter
	ContentStore content.Store
	Applier      rootfs.Applier
}

func localContainerd(root string) (*containerd, error) {
	s, err := naive.NewSnapshotter(filepath.Join(root, "snapshots"))
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

// req := &diffapi.ApplyRequest{
// 	Diff:   fromDescriptor(diff),
// 	Mounts: fromMounts(mounts),
// }
// resp, err := r.client.Apply(ctx, req)
// if err != nil {
// 	return ocispec.Descriptor{}, err
// }
// return toDescriptor(resp.Applied), nil

//	Apply(context.Context, ocispec.Descriptor, []mount.Mount) (ocispec.Descriptor, error)
