package cache

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"

	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/content/local"
	"github.com/containerd/containerd/leases"
	ctdmetadata "github.com/containerd/containerd/metadata"
	"github.com/containerd/containerd/namespaces"
	"github.com/containerd/containerd/snapshots"
	"github.com/containerd/containerd/snapshots/native"
	"github.com/moby/buildkit/cache/metadata"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/snapshot"
	containerdsnapshot "github.com/moby/buildkit/snapshot/containerd"
	"github.com/moby/buildkit/util/leaseutil"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	bolt "go.etcd.io/bbolt"
)

type cmOpt struct {
	snapshotterName string
	snapshotter     snapshots.Snapshotter
	tmpdir          string
}

type cmOut struct {
	manager Manager
	lm      leases.Manager
	cs      content.Store
}

func newCacheManager(ctx context.Context, opt cmOpt) (co *cmOut, cleanup func() error, err error) {
	ns, ok := namespaces.Namespace(ctx)
	if !ok {
		return nil, nil, errors.Errorf("namespace required for test")
	}

	if opt.snapshotterName == "" {
		opt.snapshotterName = "native"
	}

	tmpdir, err := ioutil.TempDir("", "cachemanager")
	if err != nil {
		return nil, nil, err
	}

	defers := make([]func() error, 0)
	cleanup = func() error {
		var err error
		for i := range defers {
			if err1 := defers[len(defers)-1-i](); err1 != nil && err == nil {
				err = err1
			}
		}
		return err
	}
	defer func() {
		if err != nil {
			cleanup()
		}
	}()
	if opt.tmpdir == "" {
		defers = append(defers, func() error {
			return os.RemoveAll(tmpdir)
		})
	} else {
		os.RemoveAll(tmpdir)
		tmpdir = opt.tmpdir
	}

	if opt.snapshotter == nil {
		snapshotter, err := native.NewSnapshotter(filepath.Join(tmpdir, "snapshots"))
		if err != nil {
			return nil, nil, err
		}
		opt.snapshotter = snapshotter
	}

	md, err := metadata.NewStore(filepath.Join(tmpdir, "metadata.db"))
	if err != nil {
		return nil, nil, err
	}

	store, err := local.NewStore(tmpdir)
	if err != nil {
		return nil, nil, err
	}

	db, err := bolt.Open(filepath.Join(tmpdir, "containerdmeta.db"), 0644, nil)
	if err != nil {
		return nil, nil, err
	}
	defers = append(defers, func() error {
		return db.Close()
	})

	mdb := ctdmetadata.NewDB(db, store, map[string]snapshots.Snapshotter{
		opt.snapshotterName: opt.snapshotter,
	})
	if err := mdb.Init(context.TODO()); err != nil {
		return nil, nil, err
	}

	lm := leaseutil.NewManager(mdb)

	cm, err := NewManager(ManagerOpt{
		Snapshotter:    snapshot.FromContainerdSnapshotter(opt.snapshotterName, containerdsnapshot.NSSnapshotter(ns, mdb.Snapshotter(opt.snapshotterName)), nil),
		MetadataStore:  md,
		ContentStore:   mdb.ContentStore(),
		LeaseManager:   leaseutil.WithNamespace(lm, ns),
		GarbageCollect: mdb.GarbageCollect,
	})
	if err != nil {
		return nil, nil, err
	}
	return &cmOut{
		manager: cm,
		lm:      lm,
		cs:      mdb.ContentStore(),
	}, cleanup, nil
}

func TestManager(t *testing.T) {
	t.Parallel()

	ctx := namespaces.WithNamespace(context.Background(), "buildkit-test")

	tmpdir, err := ioutil.TempDir("", "cachemanager")
	require.NoError(t, err)
	defer os.RemoveAll(tmpdir)

	snapshotter, err := native.NewSnapshotter(filepath.Join(tmpdir, "snapshots"))
	require.NoError(t, err)

	co, cleanup, err := newCacheManager(ctx, cmOpt{
		snapshotter:     snapshotter,
		snapshotterName: "native",
	})
	require.NoError(t, err)

	defer cleanup()
	cm := co.manager

	_, err = cm.Get(ctx, "foobar")
	require.Error(t, err)

	checkDiskUsage(ctx, t, cm, 0, 0)

	active, err := cm.New(ctx, nil, CachePolicyRetain)
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
	require.Equal(t, ErrLocked, errors.Cause(err))

	checkDiskUsage(ctx, t, cm, 1, 0)

	snap, err := active.Commit(ctx)
	require.NoError(t, err)

	checkDiskUsage(ctx, t, cm, 1, 0)

	_, err = cm.GetMutable(ctx, active.ID())
	require.Error(t, err)
	require.Equal(t, ErrLocked, errors.Cause(err))

	err = snap.Release(ctx)
	require.NoError(t, err)

	checkDiskUsage(ctx, t, cm, 0, 1)

	active, err = cm.GetMutable(ctx, active.ID())
	require.NoError(t, err)

	checkDiskUsage(ctx, t, cm, 1, 0)

	snap, err = active.Commit(ctx)
	require.NoError(t, err)

	checkDiskUsage(ctx, t, cm, 1, 0)

	err = snap.Finalize(ctx, true)
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

	checkDiskUsage(ctx, t, cm, 1, 0)

	err = snap.Release(ctx)
	require.NoError(t, err)

	active2, err := cm.New(ctx, snap2, CachePolicyRetain)
	require.NoError(t, err)

	checkDiskUsage(ctx, t, cm, 2, 0)

	snap3, err := active2.Commit(ctx)
	require.NoError(t, err)

	err = snap2.Release(ctx)
	require.NoError(t, err)

	checkDiskUsage(ctx, t, cm, 2, 0)

	err = snap3.Release(ctx)
	require.NoError(t, err)

	checkDiskUsage(ctx, t, cm, 0, 2)

	buf := pruneResultBuffer()
	err = cm.Prune(ctx, buf.C, client.PruneInfo{})
	buf.close()
	require.NoError(t, err)

	checkDiskUsage(ctx, t, cm, 0, 0)

	require.Equal(t, len(buf.all), 2)

	err = cm.Close()
	require.NoError(t, err)

	dirs, err := ioutil.ReadDir(filepath.Join(tmpdir, "snapshots/snapshots"))
	require.NoError(t, err)
	require.Equal(t, 0, len(dirs))
}

func TestSetBlob(t *testing.T) {
	t.Parallel()
	ctx := namespaces.WithNamespace(context.Background(), "buildkit-test")

	tmpdir, err := ioutil.TempDir("", "cachemanager")
	require.NoError(t, err)
	defer os.RemoveAll(tmpdir)

	snapshotter, err := native.NewSnapshotter(filepath.Join(tmpdir, "snapshots"))
	require.NoError(t, err)

	co, cleanup, err := newCacheManager(ctx, cmOpt{
		snapshotter:     snapshotter,
		snapshotterName: "native",
	})
	require.NoError(t, err)

	defer cleanup()

	cm := co.manager

	active, err := cm.New(ctx, nil)
	require.NoError(t, err)

	snap, err := active.Commit(ctx)
	require.NoError(t, err)

	info := snap.Info()
	require.Equal(t, "", string(info.DiffID))
	require.Equal(t, "", string(info.Blob))
	require.Equal(t, "", string(info.ChainID))
	require.Equal(t, "", string(info.BlobChainID))
	require.Equal(t, info.Extracted, true)

	ctx, clean, err := leaseutil.WithLease(ctx, co.lm)

	b, diffID, err := mapToBlob(map[string]string{"foo": "bar"})
	require.NoError(t, err)

	err = content.WriteBlob(ctx, co.cs, "ref1", bytes.NewBuffer(b), ocispec.Descriptor{})
	require.NoError(t, err)

	err = snap.SetBlob(ctx, diffID, digest.FromBytes([]byte("foobar")))
	require.Error(t, err)

	blobDgst := digest.FromBytes(b)
	err = snap.SetBlob(ctx, diffID, blobDgst)
	require.NoError(t, err)

	info = snap.Info()
	require.Equal(t, diffID, info.DiffID)
	require.Equal(t, blobDgst, info.Blob)
	require.Equal(t, diffID, info.ChainID)
	require.Equal(t, digest.FromBytes([]byte(blobDgst+" "+diffID)), info.BlobChainID)
	require.Equal(t, snap.ID(), info.SnapshotID)
	require.Equal(t, info.Extracted, true)
	blobChain1 := info.BlobChainID

	active, err = cm.New(ctx, snap)
	require.NoError(t, err)

	snap2, err := active.Commit(ctx)
	require.NoError(t, err)

	b2, diffID2, err := mapToBlob(map[string]string{"foo2": "bar2"})
	require.NoError(t, err)
	blobDgst2 := digest.FromBytes(b2)

	err = content.WriteBlob(ctx, co.cs, "ref2", bytes.NewBuffer(b2), ocispec.Descriptor{})
	require.NoError(t, err)

	err = snap2.SetBlob(ctx, diffID2, blobDgst2)
	require.NoError(t, err)

	info = snap2.Info()
	require.Equal(t, diffID2, info.DiffID)
	require.Equal(t, blobDgst2, info.Blob)
	require.Equal(t, digest.FromBytes([]byte(diffID+" "+diffID2)), info.ChainID)
	require.Equal(t, digest.FromBytes([]byte(blobChain1+" "+digest.FromBytes([]byte(blobDgst2+" "+diffID2)))), info.BlobChainID)
	require.Equal(t, snap2.ID(), info.SnapshotID)
	require.Equal(t, info.Extracted, true)

	b3, diffID3, err := mapToBlob(map[string]string{"foo3": "bar3"})
	require.NoError(t, err)
	blobDgst3 := digest.FromBytes(b3)

	err = content.WriteBlob(ctx, co.cs, "ref3", bytes.NewBuffer(b3), ocispec.Descriptor{})
	require.NoError(t, err)

	snap3, err := cm.GetByBlob(ctx, diffID3, blobDgst3, snap)
	require.NoError(t, err)

	info = snap3.Info()
	require.Equal(t, diffID3, info.DiffID)
	require.Equal(t, blobDgst3, info.Blob)
	require.Equal(t, digest.FromBytes([]byte(diffID+" "+diffID3)), info.ChainID)
	blobChain3 := digest.FromBytes([]byte(blobChain1 + " " + digest.FromBytes([]byte(blobDgst3+" "+diffID3))))
	require.Equal(t, blobChain3, info.BlobChainID)
	require.Equal(t, string(info.ChainID), info.SnapshotID)
	require.Equal(t, info.Extracted, false)

	// snap4 is same as snap2
	_, _, err = mapToBlob(map[string]string{"foo2": "bar2"})
	require.NoError(t, err)

	snap4, err := cm.GetByBlob(ctx, diffID2, blobDgst2, snap)
	require.NoError(t, err)

	require.Equal(t, snap2.ID(), snap4.ID())

	// snap5 is same different blob but same diffID as snap2
	b5, _, err := mapToBlob(map[string]string{"foo5": "bar5"})
	require.NoError(t, err)

	err = content.WriteBlob(ctx, co.cs, "ref5", bytes.NewBuffer(b5), ocispec.Descriptor{})
	require.NoError(t, err)

	blobDgst5 := digest.FromBytes(b5)
	snap5, err := cm.GetByBlob(ctx, diffID2, blobDgst5, snap)
	require.NoError(t, err)

	require.NotEqual(t, snap2.ID(), snap5.ID())
	require.Equal(t, snap2.Info().SnapshotID, snap5.Info().SnapshotID)
	require.Equal(t, diffID2, snap5.Info().DiffID)
	require.Equal(t, blobDgst5, snap5.Info().Blob)

	require.Equal(t, snap2.Info().ChainID, snap5.Info().ChainID)
	require.NotEqual(t, snap2.Info().BlobChainID, snap5.Info().BlobChainID)
	require.Equal(t, digest.FromBytes([]byte(blobChain1+" "+digest.FromBytes([]byte(blobDgst5+" "+diffID2)))), snap5.Info().BlobChainID)

	// snap6 is a child of snap3
	b6, diffID6, err := mapToBlob(map[string]string{"foo6": "bar6"})
	require.NoError(t, err)
	blobDgst6 := digest.FromBytes(b6)

	err = content.WriteBlob(ctx, co.cs, "ref6", bytes.NewBuffer(b6), ocispec.Descriptor{})
	require.NoError(t, err)

	snap6, err := cm.GetByBlob(ctx, diffID6, blobDgst6, snap3)
	require.NoError(t, err)

	info = snap6.Info()
	require.Equal(t, diffID6, info.DiffID)
	require.Equal(t, blobDgst6, info.Blob)
	require.Equal(t, digest.FromBytes([]byte(snap3.Info().ChainID+" "+diffID6)), info.ChainID)
	require.Equal(t, digest.FromBytes([]byte(blobChain3+" "+digest.FromBytes([]byte(blobDgst6+" "+diffID6)))), info.BlobChainID)
	require.Equal(t, string(info.ChainID), info.SnapshotID)
	require.Equal(t, info.Extracted, false)

	_, err = cm.GetByBlob(ctx, diffID6, digest.FromBytes([]byte("notexist")), snap3)
	require.Error(t, err)

	clean(context.TODO())

	//snap.SetBlob()
}

func TestPrune(t *testing.T) {
	t.Parallel()
	ctx := namespaces.WithNamespace(context.Background(), "buildkit-test")

	tmpdir, err := ioutil.TempDir("", "cachemanager")
	require.NoError(t, err)
	defer os.RemoveAll(tmpdir)

	snapshotter, err := native.NewSnapshotter(filepath.Join(tmpdir, "snapshots"))
	require.NoError(t, err)

	co, cleanup, err := newCacheManager(ctx, cmOpt{
		snapshotter:     snapshotter,
		snapshotterName: "native",
	})
	require.NoError(t, err)

	defer cleanup()
	cm := co.manager

	active, err := cm.New(ctx, nil)
	require.NoError(t, err)

	snap, err := active.Commit(ctx)
	require.NoError(t, err)

	active, err = cm.New(ctx, snap, CachePolicyRetain)
	require.NoError(t, err)

	snap2, err := active.Commit(ctx)
	require.NoError(t, err)

	checkDiskUsage(ctx, t, cm, 2, 0)

	dirs, err := ioutil.ReadDir(filepath.Join(tmpdir, "snapshots/snapshots"))
	require.NoError(t, err)
	require.Equal(t, 2, len(dirs))

	// prune with keeping refs does nothing
	buf := pruneResultBuffer()
	err = cm.Prune(ctx, buf.C, client.PruneInfo{})
	buf.close()
	require.NoError(t, err)

	checkDiskUsage(ctx, t, cm, 2, 0)
	require.Equal(t, len(buf.all), 0)

	dirs, err = ioutil.ReadDir(filepath.Join(tmpdir, "snapshots/snapshots"))
	require.NoError(t, err)
	require.Equal(t, 2, len(dirs))

	err = snap2.Release(ctx)
	require.NoError(t, err)

	checkDiskUsage(ctx, t, cm, 1, 1)

	// prune with keeping single refs deletes one
	buf = pruneResultBuffer()
	err = cm.Prune(ctx, buf.C, client.PruneInfo{})
	buf.close()
	require.NoError(t, err)

	checkDiskUsage(ctx, t, cm, 1, 0)
	require.Equal(t, len(buf.all), 1)

	dirs, err = ioutil.ReadDir(filepath.Join(tmpdir, "snapshots/snapshots"))
	require.NoError(t, err)
	require.Equal(t, 1, len(dirs))

	err = snap.Release(ctx)
	require.NoError(t, err)

	active, err = cm.New(ctx, snap, CachePolicyRetain)
	require.NoError(t, err)

	snap2, err = active.Commit(ctx)
	require.NoError(t, err)

	err = snap.Release(ctx)
	require.NoError(t, err)

	checkDiskUsage(ctx, t, cm, 2, 0)

	// prune with parent released does nothing
	buf = pruneResultBuffer()
	err = cm.Prune(ctx, buf.C, client.PruneInfo{})
	buf.close()
	require.NoError(t, err)

	checkDiskUsage(ctx, t, cm, 2, 0)
	require.Equal(t, len(buf.all), 0)

	// releasing last reference
	err = snap2.Release(ctx)
	require.NoError(t, err)
	checkDiskUsage(ctx, t, cm, 0, 2)

	buf = pruneResultBuffer()
	err = cm.Prune(ctx, buf.C, client.PruneInfo{})
	buf.close()
	require.NoError(t, err)

	checkDiskUsage(ctx, t, cm, 0, 0)
	require.Equal(t, len(buf.all), 2)

	dirs, err = ioutil.ReadDir(filepath.Join(tmpdir, "snapshots/snapshots"))
	require.NoError(t, err)
	require.Equal(t, 0, len(dirs))
}

func TestLazyCommit(t *testing.T) {
	t.Parallel()

	ctx := namespaces.WithNamespace(context.Background(), "buildkit-test")

	tmpdir, err := ioutil.TempDir("", "cachemanager")
	require.NoError(t, err)
	defer os.RemoveAll(tmpdir)

	snapshotter, err := native.NewSnapshotter(filepath.Join(tmpdir, "snapshots"))
	require.NoError(t, err)

	co, cleanup, err := newCacheManager(ctx, cmOpt{
		tmpdir:          tmpdir,
		snapshotter:     snapshotter,
		snapshotterName: "native",
	})
	require.NoError(t, err)
	cm := co.manager

	active, err := cm.New(ctx, nil, CachePolicyRetain)
	require.NoError(t, err)

	// after commit mutable is locked
	snap, err := active.Commit(ctx)
	require.NoError(t, err)

	_, err = cm.GetMutable(ctx, active.ID())
	require.Error(t, err)
	require.Equal(t, ErrLocked, errors.Cause(err))

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

	// active can't be get while immutable is held
	_, err = cm.GetMutable(ctx, active.ID())
	require.Error(t, err)
	require.Equal(t, ErrLocked, errors.Cause(err))

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
	err = snap.Finalize(ctx, true)
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
	active, err = cm.New(ctx, nil, CachePolicyRetain)
	require.NoError(t, err)

	// after commit mutable is locked
	snap, err = active.Commit(ctx)
	require.NoError(t, err)

	err = cm.Close()
	require.NoError(t, err)

	cleanup()

	// we can't close snapshotter and open it twice (especially, its internal bbolt store)
	co, cleanup, err = newCacheManager(ctx, cmOpt{
		tmpdir:          tmpdir,
		snapshotter:     snapshotter,
		snapshotterName: "native",
	})
	require.NoError(t, err)
	cm = co.manager

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

	cleanup()

	co, cleanup, err = newCacheManager(ctx, cmOpt{
		tmpdir:          tmpdir,
		snapshotter:     snapshotter,
		snapshotterName: "native",
	})
	require.NoError(t, err)
	defer cleanup()
	cm = co.manager

	snap2, err = cm.Get(ctx, snap.ID())
	require.NoError(t, err)

	err = snap2.Finalize(ctx, true)
	require.NoError(t, err)

	err = snap2.Release(ctx)
	require.NoError(t, err)

	active, err = cm.GetMutable(ctx, active.ID())
	require.Error(t, err)
	require.Equal(t, errNotFound, errors.Cause(err))
}

func checkDiskUsage(ctx context.Context, t *testing.T, cm Manager, inuse, unused int) {
	du, err := cm.DiskUsage(ctx, client.DiskUsageInfo{})
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

func pruneResultBuffer() *buf {
	b := &buf{C: make(chan client.UsageInfo), closed: make(chan struct{})}
	go func() {
		for c := range b.C {
			b.all = append(b.all, c)
		}
		close(b.closed)
	}()
	return b
}

type buf struct {
	C      chan client.UsageInfo
	closed chan struct{}
	all    []client.UsageInfo
}

func (b *buf) close() {
	close(b.C)
	<-b.closed
}

func mapToBlob(m map[string]string) ([]byte, digest.Digest, error) {
	buf := bytes.NewBuffer(nil)
	gz := gzip.NewWriter(buf)
	sha := digest.SHA256.Digester()
	tw := tar.NewWriter(io.MultiWriter(sha.Hash(), gz))

	for k, v := range m {
		if err := tw.WriteHeader(&tar.Header{
			Name: k,
			Size: int64(len(v)),
		}); err != nil {
			return nil, "", err
		}
		if _, err := tw.Write([]byte(v)); err != nil {
			return nil, "", err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, "", err
	}
	if err := gz.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), sha.Digest(), nil
}
