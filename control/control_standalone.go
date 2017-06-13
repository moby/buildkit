// +build standalone

package control

import (
	"io"
	"io/ioutil"
	"os"
	"path/filepath"

	"github.com/containerd/containerd/archive"
	"github.com/containerd/containerd/archive/compression"
	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/mount"
	"github.com/containerd/containerd/snapshot/overlay"
	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/tonistiigi/buildkit_poc/worker/runcworker"
	"golang.org/x/net/context"
)

func NewStandalone(root string) (*Controller, error) {
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, errors.Wrapf(err, "failed to create %s", root)
	}

	// TODO: take lock to make sure there are no duplicates

	pd, err := newPullDeps(root)
	if err != nil {
		return nil, err
	}

	opt, err := defaultControllerOpts(root, *pd)
	if err != nil {
		return nil, err
	}

	w, err := runcworker.New(filepath.Join(root, "runc"))
	if err != nil {
		return nil, err
	}

	opt.Worker = w

	return NewController(*opt)
}

func newPullDeps(root string) (*pullDeps, error) {
	s, err := overlay.NewSnapshotter(filepath.Join(root, "snapshots"))
	if err != nil {
		return nil, err
	}

	c, err := content.NewStore(filepath.Join(root, "content"))
	if err != nil {
		return nil, err
	}

	a := &localApplier{root: root, content: c}

	return &pullDeps{
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

type nopCloser struct {
	io.Writer
}

func (n *nopCloser) Close() error {
	return nil
}
