package cache

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

	ctdcompression "github.com/containerd/containerd/archive/compression"
	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/content/local"
	"github.com/containerd/containerd/diff/apply"
	"github.com/containerd/containerd/diff/walking"
	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/leases"
	ctdmetadata "github.com/containerd/containerd/metadata"
	"github.com/containerd/containerd/namespaces"
	"github.com/containerd/containerd/snapshots"
	"github.com/containerd/containerd/snapshots/native"
	"github.com/containerd/stargz-snapshotter/estargz"
	"github.com/klauspost/compress/zstd"
	"github.com/moby/buildkit/cache/metadata"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/snapshot"
	containerdsnapshot "github.com/moby/buildkit/snapshot/containerd"
	"github.com/moby/buildkit/util/compression"
	"github.com/moby/buildkit/util/contentutil"
	"github.com/moby/buildkit/util/leaseutil"
	"github.com/moby/buildkit/util/winlayers"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	bolt "go.etcd.io/bbolt"
	"golang.org/x/sync/errgroup"
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
		if err != nil && cleanup != nil {
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

	store = containerdsnapshot.NewContentStore(mdb.ContentStore(), ns)
	lm := leaseutil.WithNamespace(ctdmetadata.NewLeaseManager(mdb), ns)

	md, err := metadata.NewStore(filepath.Join(tmpdir, "metadata.db"))
	if err != nil {
		return nil, nil, err
	}

	cm, err := NewManager(ManagerOpt{
		Snapshotter:    snapshot.FromContainerdSnapshotter(opt.snapshotterName, containerdsnapshot.NSSnapshotter(ns, mdb.Snapshotter(opt.snapshotterName)), nil),
		MetadataStore:  md,
		ContentStore:   store,
		LeaseManager:   lm,
		GarbageCollect: mdb.GarbageCollect,
		Applier:        winlayers.NewFileSystemApplierWithWindows(store, apply.NewFileSystemApplier(store)),
		Differ:         winlayers.NewWalkingDiffWithWindows(store, walking.NewWalkingDiff(store)),
	})
	if err != nil {
		return nil, nil, err
	}
	return &cmOut{
		manager: cm,
		lm:      lm,
		cs:      store,
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

	active, err := cm.New(ctx, nil, nil, CachePolicyRetain)
	require.NoError(t, err)

	m, err := active.Mount(ctx, false, nil)
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
	require.Equal(t, true, errors.Is(err, ErrLocked))

	checkDiskUsage(ctx, t, cm, 1, 0)

	snap, err := active.Commit(ctx)
	require.NoError(t, err)

	checkDiskUsage(ctx, t, cm, 1, 0)

	_, err = cm.GetMutable(ctx, active.ID())
	require.Error(t, err)
	require.Equal(t, true, errors.Is(err, ErrLocked))

	err = snap.Release(ctx)
	require.NoError(t, err)

	checkDiskUsage(ctx, t, cm, 0, 1)

	active, err = cm.GetMutable(ctx, active.ID())
	require.NoError(t, err)

	checkDiskUsage(ctx, t, cm, 1, 0)

	snap, err = active.Commit(ctx)
	require.NoError(t, err)

	checkDiskUsage(ctx, t, cm, 1, 0)

	err = snap.(*immutableRef).finalizeLocked(ctx)
	require.NoError(t, err)

	err = snap.Release(ctx)
	require.NoError(t, err)

	_, err = cm.GetMutable(ctx, active.ID())
	require.Error(t, err)
	require.Equal(t, true, errors.Is(err, errNotFound))

	_, err = cm.GetMutable(ctx, snap.ID())
	require.Error(t, err)
	require.Equal(t, true, errors.Is(err, errInvalid))

	snap, err = cm.Get(ctx, snap.ID())
	require.NoError(t, err)

	snap2, err := cm.Get(ctx, snap.ID())
	require.NoError(t, err)

	checkDiskUsage(ctx, t, cm, 1, 0)

	err = snap.Release(ctx)
	require.NoError(t, err)

	active2, err := cm.New(ctx, snap2, nil, CachePolicyRetain)
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

func TestLazyGetByBlob(t *testing.T) {
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

	// Test for #2226 https://github.com/moby/buildkit/issues/2226, create lazy blobs with the same diff ID but
	// different digests (due to different compression) and make sure GetByBlob still works
	_, desc, err := mapToBlob(map[string]string{"foo": "bar"}, true)
	require.NoError(t, err)
	descHandlers := DescHandlers(make(map[digest.Digest]*DescHandler))
	descHandlers[desc.Digest] = &DescHandler{}
	diffID, err := diffIDFromDescriptor(desc)
	require.NoError(t, err)

	_, err = cm.GetByBlob(ctx, desc, nil, descHandlers)
	require.NoError(t, err)

	_, desc2, err := mapToBlob(map[string]string{"foo": "bar"}, false)
	require.NoError(t, err)
	descHandlers2 := DescHandlers(make(map[digest.Digest]*DescHandler))
	descHandlers2[desc2.Digest] = &DescHandler{}
	diffID2, err := diffIDFromDescriptor(desc2)
	require.NoError(t, err)

	require.NotEqual(t, desc.Digest, desc2.Digest)
	require.Equal(t, diffID, diffID2)

	_, err = cm.GetByBlob(ctx, desc2, nil, descHandlers2)
	require.NoError(t, err)
}

func TestSnapshotExtract(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Depends on unimplemented containerd bind-mount support on Windows")
	}

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

	b, desc, err := mapToBlob(map[string]string{"foo": "bar"}, true)
	require.NoError(t, err)

	err = content.WriteBlob(ctx, co.cs, "ref1", bytes.NewBuffer(b), desc)
	require.NoError(t, err)

	snap, err := cm.GetByBlob(ctx, desc, nil)
	require.NoError(t, err)

	require.Equal(t, false, !snap.(*immutableRef).getBlobOnly())

	b2, desc2, err := mapToBlob(map[string]string{"foo": "bar123"}, true)
	require.NoError(t, err)

	err = content.WriteBlob(ctx, co.cs, "ref1", bytes.NewBuffer(b2), desc2)
	require.NoError(t, err)

	snap2, err := cm.GetByBlob(ctx, desc2, snap)
	require.NoError(t, err)

	size, err := snap2.(*immutableRef).size(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(len(b2)), size)

	require.Equal(t, false, !snap2.(*immutableRef).getBlobOnly())

	dirs, err := ioutil.ReadDir(filepath.Join(tmpdir, "snapshots/snapshots"))
	require.NoError(t, err)
	require.Equal(t, 0, len(dirs))

	checkNumBlobs(ctx, t, co.cs, 2)

	err = snap2.Extract(ctx, nil)
	require.NoError(t, err)

	require.Equal(t, true, !snap.(*immutableRef).getBlobOnly())
	require.Equal(t, true, !snap2.(*immutableRef).getBlobOnly())

	dirs, err = ioutil.ReadDir(filepath.Join(tmpdir, "snapshots/snapshots"))
	require.NoError(t, err)
	require.Equal(t, 2, len(dirs))

	buf := pruneResultBuffer()
	err = cm.Prune(ctx, buf.C, client.PruneInfo{})
	buf.close()
	require.NoError(t, err)

	checkDiskUsage(ctx, t, cm, 2, 0)

	require.Equal(t, len(buf.all), 0)

	dirs, err = ioutil.ReadDir(filepath.Join(tmpdir, "snapshots/snapshots"))
	require.NoError(t, err)
	require.Equal(t, 2, len(dirs))

	checkNumBlobs(ctx, t, co.cs, 2)

	id := snap.ID()

	err = snap.Release(context.TODO())
	require.NoError(t, err)

	buf = pruneResultBuffer()
	err = cm.Prune(ctx, buf.C, client.PruneInfo{})
	buf.close()
	require.NoError(t, err)

	checkDiskUsage(ctx, t, cm, 2, 0)

	dirs, err = ioutil.ReadDir(filepath.Join(tmpdir, "snapshots/snapshots"))
	require.NoError(t, err)
	require.Equal(t, 2, len(dirs))

	snap, err = cm.Get(ctx, id)
	require.NoError(t, err)

	checkDiskUsage(ctx, t, cm, 2, 0)

	err = snap2.Release(context.TODO())
	require.NoError(t, err)

	checkDiskUsage(ctx, t, cm, 1, 1)

	buf = pruneResultBuffer()
	err = cm.Prune(ctx, buf.C, client.PruneInfo{})
	buf.close()
	require.NoError(t, err)

	checkDiskUsage(ctx, t, cm, 1, 0)

	require.Equal(t, len(buf.all), 1)

	dirs, err = ioutil.ReadDir(filepath.Join(tmpdir, "snapshots/snapshots"))
	require.NoError(t, err)
	require.Equal(t, 1, len(dirs))

	checkNumBlobs(ctx, t, co.cs, 1)

	err = snap.Release(context.TODO())
	require.NoError(t, err)

	buf = pruneResultBuffer()
	err = cm.Prune(ctx, buf.C, client.PruneInfo{})
	buf.close()
	require.NoError(t, err)

	checkDiskUsage(ctx, t, cm, 0, 0)

	dirs, err = ioutil.ReadDir(filepath.Join(tmpdir, "snapshots/snapshots"))
	require.NoError(t, err)
	require.Equal(t, 0, len(dirs))

	checkNumBlobs(ctx, t, co.cs, 0)
}

func TestExtractOnMutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Depends on unimplemented containerd bind-mount support on Windows")
	}

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

	active, err := cm.New(ctx, nil, nil)
	require.NoError(t, err)

	snap, err := active.Commit(ctx)
	require.NoError(t, err)

	b, desc, err := mapToBlob(map[string]string{"foo": "bar"}, true)
	require.NoError(t, err)

	err = content.WriteBlob(ctx, co.cs, "ref1", bytes.NewBuffer(b), desc)
	require.NoError(t, err)

	b2, desc2, err := mapToBlob(map[string]string{"foo2": "1"}, true)
	require.NoError(t, err)

	err = content.WriteBlob(ctx, co.cs, "ref2", bytes.NewBuffer(b2), desc2)
	require.NoError(t, err)

	_, err = cm.GetByBlob(ctx, desc2, snap)
	require.Error(t, err)

	leaseCtx, done, err := leaseutil.WithLease(ctx, co.lm, leases.WithExpiration(0))
	require.NoError(t, err)

	compressionType := compression.FromMediaType(desc.MediaType)
	if compressionType == compression.UnknownCompression {
		t.Errorf("unhandled layer media type: %q", desc.MediaType)
	}
	err = snap.(*immutableRef).setBlob(leaseCtx, compressionType, desc)
	done(context.TODO())
	require.NoError(t, err)
	err = snap.(*immutableRef).setChains(leaseCtx)
	require.NoError(t, err)

	snap2, err := cm.GetByBlob(ctx, desc2, snap)
	require.NoError(t, err)

	err = snap.Release(context.TODO())
	require.NoError(t, err)

	require.Equal(t, false, !snap2.(*immutableRef).getBlobOnly())

	size, err := snap2.(*immutableRef).size(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(len(b2)), size)

	dirs, err := ioutil.ReadDir(filepath.Join(tmpdir, "snapshots/snapshots"))
	require.NoError(t, err)
	require.Equal(t, 1, len(dirs))

	checkNumBlobs(ctx, t, co.cs, 2)

	err = snap2.Extract(ctx, nil)
	require.NoError(t, err)

	require.Equal(t, true, !snap.(*immutableRef).getBlobOnly())
	require.Equal(t, true, !snap2.(*immutableRef).getBlobOnly())

	buf := pruneResultBuffer()
	err = cm.Prune(ctx, buf.C, client.PruneInfo{})
	buf.close()
	require.NoError(t, err)

	checkDiskUsage(ctx, t, cm, 2, 0)

	require.Equal(t, len(buf.all), 0)

	dirs, err = ioutil.ReadDir(filepath.Join(tmpdir, "snapshots/snapshots"))
	require.NoError(t, err)
	require.Equal(t, 2, len(dirs))

	err = snap2.Release(context.TODO())
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

	checkNumBlobs(ctx, t, co.cs, 0)
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

	ctx, done, err := leaseutil.WithLease(ctx, co.lm, leaseutil.MakeTemporary)
	require.NoError(t, err)
	defer done(context.TODO())

	cm := co.manager

	active, err := cm.New(ctx, nil, nil)
	require.NoError(t, err)

	snap, err := active.Commit(ctx)
	require.NoError(t, err)

	snapRef := snap.(*immutableRef)
	require.Equal(t, "", string(snapRef.getDiffID()))
	require.Equal(t, "", string(snapRef.getBlob()))
	require.Equal(t, "", string(snapRef.getChainID()))
	require.Equal(t, "", string(snapRef.getBlobChainID()))
	require.Equal(t, !snapRef.getBlobOnly(), true)

	ctx, clean, err := leaseutil.WithLease(ctx, co.lm)
	require.NoError(t, err)
	defer clean(context.TODO())

	b, desc, err := mapToBlob(map[string]string{"foo": "bar"}, true)
	require.NoError(t, err)

	err = content.WriteBlob(ctx, co.cs, "ref1", bytes.NewBuffer(b), desc)
	require.NoError(t, err)

	err = snap.(*immutableRef).setBlob(ctx, compression.UnknownCompression, ocispecs.Descriptor{
		Digest: digest.FromBytes([]byte("foobar")),
		Annotations: map[string]string{
			"containerd.io/uncompressed": digest.FromBytes([]byte("foobar2")).String(),
		},
	})
	require.Error(t, err)

	compressionType := compression.FromMediaType(desc.MediaType)
	if compressionType == compression.UnknownCompression {
		t.Errorf("unhandled layer media type: %q", desc.MediaType)
	}
	err = snap.(*immutableRef).setBlob(ctx, compressionType, desc)
	require.NoError(t, err)
	err = snap.(*immutableRef).setChains(ctx)
	require.NoError(t, err)

	snapRef = snap.(*immutableRef)
	require.Equal(t, desc.Annotations["containerd.io/uncompressed"], string(snapRef.getDiffID()))
	require.Equal(t, desc.Digest, snapRef.getBlob())
	require.Equal(t, desc.MediaType, snapRef.getMediaType())
	require.Equal(t, snapRef.getDiffID(), snapRef.getChainID())
	require.Equal(t, digest.FromBytes([]byte(desc.Digest+" "+snapRef.getDiffID())), snapRef.getBlobChainID())
	require.Equal(t, snap.ID(), snapRef.getSnapshotID())
	require.Equal(t, !snapRef.getBlobOnly(), true)

	active, err = cm.New(ctx, snap, nil)
	require.NoError(t, err)

	snap2, err := active.Commit(ctx)
	require.NoError(t, err)

	b2, desc2, err := mapToBlob(map[string]string{"foo2": "bar2"}, true)
	require.NoError(t, err)

	err = content.WriteBlob(ctx, co.cs, "ref2", bytes.NewBuffer(b2), desc2)
	require.NoError(t, err)

	compressionType2 := compression.FromMediaType(desc2.MediaType)
	if compressionType2 == compression.UnknownCompression {
		t.Errorf("unhandled layer media type: %q", desc2.MediaType)
	}
	err = snap2.(*immutableRef).setBlob(ctx, compressionType2, desc2)
	require.NoError(t, err)
	err = snap2.(*immutableRef).setChains(ctx)
	require.NoError(t, err)

	snapRef2 := snap2.(*immutableRef)
	require.Equal(t, desc2.Annotations["containerd.io/uncompressed"], string(snapRef2.getDiffID()))
	require.Equal(t, desc2.Digest, snapRef2.getBlob())
	require.Equal(t, desc2.MediaType, snapRef2.getMediaType())
	require.Equal(t, digest.FromBytes([]byte(snapRef.getChainID()+" "+snapRef2.getDiffID())), snapRef2.getChainID())
	require.Equal(t, digest.FromBytes([]byte(snapRef.getBlobChainID()+" "+digest.FromBytes([]byte(desc2.Digest+" "+snapRef2.getDiffID())))), snapRef2.getBlobChainID())
	require.Equal(t, snap2.ID(), snapRef2.getSnapshotID())
	require.Equal(t, !snapRef2.getBlobOnly(), true)

	b3, desc3, err := mapToBlob(map[string]string{"foo3": "bar3"}, true)
	require.NoError(t, err)

	err = content.WriteBlob(ctx, co.cs, "ref3", bytes.NewBuffer(b3), desc3)
	require.NoError(t, err)

	snap3, err := cm.GetByBlob(ctx, desc3, snap)
	require.NoError(t, err)

	snapRef3 := snap3.(*immutableRef)
	require.Equal(t, desc3.Annotations["containerd.io/uncompressed"], string(snapRef3.getDiffID()))
	require.Equal(t, desc3.Digest, snapRef3.getBlob())
	require.Equal(t, desc3.MediaType, snapRef3.getMediaType())
	require.Equal(t, digest.FromBytes([]byte(snapRef.getChainID()+" "+snapRef3.getDiffID())), snapRef3.getChainID())
	require.Equal(t, digest.FromBytes([]byte(snapRef.getBlobChainID()+" "+digest.FromBytes([]byte(desc3.Digest+" "+snapRef3.getDiffID())))), snapRef3.getBlobChainID())
	require.Equal(t, string(snapRef3.getChainID()), snapRef3.getSnapshotID())
	require.Equal(t, !snapRef3.getBlobOnly(), false)

	// snap4 is same as snap2
	snap4, err := cm.GetByBlob(ctx, desc2, snap)
	require.NoError(t, err)

	require.Equal(t, snap2.ID(), snap4.ID())

	// snap5 is same different blob but same diffID as snap2
	b5, desc5, err := mapToBlob(map[string]string{"foo5": "bar5"}, true)
	require.NoError(t, err)

	desc5.Annotations["containerd.io/uncompressed"] = snapRef2.getDiffID().String()

	err = content.WriteBlob(ctx, co.cs, "ref5", bytes.NewBuffer(b5), desc5)
	require.NoError(t, err)

	snap5, err := cm.GetByBlob(ctx, desc5, snap)
	require.NoError(t, err)

	snapRef5 := snap5.(*immutableRef)
	require.NotEqual(t, snap2.ID(), snap5.ID())
	require.Equal(t, snapRef2.getSnapshotID(), snapRef5.getSnapshotID())
	require.Equal(t, snapRef2.getDiffID(), snapRef5.getDiffID())
	require.Equal(t, desc5.Digest, snapRef5.getBlob())

	require.Equal(t, snapRef2.getChainID(), snapRef5.getChainID())
	require.NotEqual(t, snapRef2.getBlobChainID(), snapRef5.getBlobChainID())
	require.Equal(t, digest.FromBytes([]byte(snapRef.getBlobChainID()+" "+digest.FromBytes([]byte(desc5.Digest+" "+snapRef2.getDiffID())))), snapRef5.getBlobChainID())

	// snap6 is a child of snap3
	b6, desc6, err := mapToBlob(map[string]string{"foo6": "bar6"}, true)
	require.NoError(t, err)

	err = content.WriteBlob(ctx, co.cs, "ref6", bytes.NewBuffer(b6), desc6)
	require.NoError(t, err)

	snap6, err := cm.GetByBlob(ctx, desc6, snap3)
	require.NoError(t, err)

	snapRef6 := snap6.(*immutableRef)
	require.Equal(t, desc6.Annotations["containerd.io/uncompressed"], string(snapRef6.getDiffID()))
	require.Equal(t, desc6.Digest, snapRef6.getBlob())
	require.Equal(t, digest.FromBytes([]byte(snapRef3.getChainID()+" "+snapRef6.getDiffID())), snapRef6.getChainID())
	require.Equal(t, digest.FromBytes([]byte(snapRef3.getBlobChainID()+" "+digest.FromBytes([]byte(snapRef6.getBlob()+" "+snapRef6.getDiffID())))), snapRef6.getBlobChainID())
	require.Equal(t, string(snapRef6.getChainID()), snapRef6.getSnapshotID())
	require.Equal(t, !snapRef6.getBlobOnly(), false)

	_, err = cm.GetByBlob(ctx, ocispecs.Descriptor{
		Digest: digest.FromBytes([]byte("notexist")),
		Annotations: map[string]string{
			"containerd.io/uncompressed": digest.FromBytes([]byte("notexist")).String(),
		},
	}, snap3)
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

	active, err := cm.New(ctx, nil, nil)
	require.NoError(t, err)

	snap, err := active.Commit(ctx)
	require.NoError(t, err)

	active, err = cm.New(ctx, snap, nil, CachePolicyRetain)
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

	active, err = cm.New(ctx, snap, nil, CachePolicyRetain)
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

	active, err := cm.New(ctx, nil, nil, CachePolicyRetain)
	require.NoError(t, err)

	// after commit mutable is locked
	snap, err := active.Commit(ctx)
	require.NoError(t, err)

	_, err = cm.GetMutable(ctx, active.ID())
	require.Error(t, err)
	require.Equal(t, true, errors.Is(err, ErrLocked))

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
	require.Equal(t, true, errors.Is(err, ErrLocked))

	err = snap.Release(ctx)
	require.NoError(t, err)

	// after release mutable becomes available again
	active2, err := cm.GetMutable(ctx, active.ID())
	require.NoError(t, err)
	require.Equal(t, active2.ID(), active.ID())

	// because ref was took mutable old immutable are cleared
	_, err = cm.Get(ctx, snap.ID())
	require.Error(t, err)
	require.Equal(t, true, errors.Is(err, errNotFound))

	snap, err = active2.Commit(ctx)
	require.NoError(t, err)

	// this time finalize commit
	err = snap.(*immutableRef).finalizeLocked(ctx)
	require.NoError(t, err)

	err = snap.Release(ctx)
	require.NoError(t, err)

	// mutable is gone after finalize
	_, err = cm.GetMutable(ctx, active2.ID())
	require.Error(t, err)
	require.Equal(t, true, errors.Is(err, errNotFound))

	// immutable still works
	snap2, err = cm.Get(ctx, snap.ID())
	require.NoError(t, err)
	require.Equal(t, snap.ID(), snap2.ID())

	err = snap2.Release(ctx)
	require.NoError(t, err)

	// test restarting after commit
	active, err = cm.New(ctx, nil, nil, CachePolicyRetain)
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
	require.Equal(t, true, errors.Is(err, errNotFound))

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

	err = snap2.(*immutableRef).finalizeLocked(ctx)
	require.NoError(t, err)

	err = snap2.Release(ctx)
	require.NoError(t, err)

	_, err = cm.GetMutable(ctx, active.ID())
	require.Error(t, err)
	require.Equal(t, true, errors.Is(err, errNotFound))
}

func TestGetRemote(t *testing.T) {
	t.Parallel()
	// windows fails when lazy blob is being extracted with "invalid windows mount type: 'bind'"
	if runtime.GOOS != "linux" {
		t.Skipf("unsupported GOOS: %s", runtime.GOOS)
	}

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

	ctx, done, err := leaseutil.WithLease(ctx, co.lm, leaseutil.MakeTemporary)
	require.NoError(t, err)
	defer done(context.TODO())

	contentBuffer := contentutil.NewBuffer()

	descHandlers := DescHandlers(map[digest.Digest]*DescHandler{})

	// make some lazy refs from blobs
	expectedContent := map[digest.Digest]struct{}{}
	esgz2gzip := map[digest.Digest]digest.Digest{}
	var descs []ocispecs.Descriptor
	for i := 0; i < 2; i++ {
		blobmap := map[string]string{"foo": strconv.Itoa(i)}
		blobBytes, desc, err := mapToBlob(blobmap, true)
		require.NoError(t, err)

		expectedContent[desc.Digest] = struct{}{}
		descs = append(descs, desc)

		cw, err := contentBuffer.Writer(ctx)
		require.NoError(t, err)
		_, err = cw.Write(blobBytes)
		require.NoError(t, err)
		err = cw.Commit(ctx, 0, cw.Digest())
		require.NoError(t, err)

		descHandlers[desc.Digest] = &DescHandler{
			Provider: func(_ session.Group) content.Provider { return contentBuffer },
		}

		uncompressedBlobBytes, uncompressedDesc, err := mapToBlob(blobmap, false)
		require.NoError(t, err)
		expectedContent[uncompressedDesc.Digest] = struct{}{}

		esgzDgst, err := esgzBlobDigest(uncompressedBlobBytes)
		require.NoError(t, err)
		// expectedContent[esgzDgst] = struct{}{} // disabled
		esgz2gzip[esgzDgst] = desc.Digest

		zstdDigest, err := zstdBlobDigest(uncompressedBlobBytes)
		require.NoError(t, err)
		expectedContent[zstdDigest] = struct{}{}
	}

	// Create 3 levels of mutable refs, where each parent ref has 2 children (this tests parallel creation of
	// overlapping blob chains).
	lazyRef, err := cm.GetByBlob(ctx, descs[0], nil, descHandlers)
	require.NoError(t, err)

	refs := []ImmutableRef{lazyRef}
	for i := 0; i < 3; i++ {
		var newRefs []ImmutableRef
		for j, ir := range refs {
			for k := 0; k < 2; k++ {
				mutRef, err := cm.New(ctx, ir, nil, descHandlers)
				require.NoError(t, err)

				m, err := mutRef.Mount(ctx, false, nil)
				require.NoError(t, err)

				lm := snapshot.LocalMounter(m)
				target, err := lm.Mount()
				require.NoError(t, err)

				f, err := os.Create(filepath.Join(target, fmt.Sprintf("%d-%d-%d", i, j, k)))
				require.NoError(t, err)
				err = os.Chtimes(f.Name(), time.Unix(0, 0), time.Unix(0, 0))
				require.NoError(t, err)

				_, desc, err := fileToBlob(f, true)
				require.NoError(t, err)
				expectedContent[desc.Digest] = struct{}{}
				uncompressedBlobBytes, uncompressedDesc, err := fileToBlob(f, false)
				require.NoError(t, err)
				expectedContent[uncompressedDesc.Digest] = struct{}{}
				esgzDgst, err := esgzBlobDigest(uncompressedBlobBytes)
				require.NoError(t, err)
				// expectedContent[esgzDgst] = struct{}{}
				esgz2gzip[esgzDgst] = desc.Digest

				zstdDigest, err := zstdBlobDigest(uncompressedBlobBytes)
				require.NoError(t, err)
				expectedContent[zstdDigest] = struct{}{}

				f.Close()
				err = lm.Unmount()
				require.NoError(t, err)

				immutRef, err := mutRef.Commit(ctx)
				require.NoError(t, err)
				newRefs = append(newRefs, immutRef)
			}
		}
		refs = newRefs
	}

	// also test the original lazyRef to get coverage for refs that don't have to be extracted from the snapshotter
	lazyRef2, err := cm.GetByBlob(ctx, descs[1], nil, descHandlers)
	require.NoError(t, err)
	refs = append(refs, lazyRef2)

	checkNumBlobs(ctx, t, co.cs, 1)

	// Call GetRemote on all the refs
	esgzRefs := map[digest.Digest]struct{}{}
	var esgzRefsMu sync.Mutex
	eg, egctx := errgroup.WithContext(ctx)
	for _, ir := range refs {
		ir := ir.(*immutableRef)
		for _, compressionType := range []compression.Type{compression.Uncompressed, compression.Gzip /*compression.EStargz,*/, compression.Zstd} {
			compressionType := compressionType
			eg.Go(func() error {
				remote, err := ir.GetRemote(egctx, true, CompressionOpt{
					Type:  compressionType,
					Force: true,
					Level: -1,
				}, nil)
				require.NoError(t, err)
				refChain := ir.parentRefChain()
				for i, desc := range remote.Descriptors {
					switch compressionType {
					case compression.Uncompressed:
						require.Equal(t, ocispecs.MediaTypeImageLayer, desc.MediaType)
					case compression.Gzip:
						require.Equal(t, ocispecs.MediaTypeImageLayerGzip, desc.MediaType)
					case compression.EStargz:
						require.Equal(t, ocispecs.MediaTypeImageLayerGzip, desc.MediaType)
					case compression.Zstd:
						require.Equal(t, ocispecs.MediaTypeImageLayer+"+zstd", desc.MediaType)
					default:
						require.Fail(t, "unhandled media type", compressionType)
					}
					dgst := desc.Digest
					require.Contains(t, expectedContent, dgst, "for %v", compressionType)
					checkDescriptor(ctx, t, co.cs, desc, compressionType)

					r := refChain[i]
					if compressionType == compression.EStargz {
						if digest.Digest(r.getBlob()) == desc.Digest {
							esgzRefsMu.Lock()
							esgzRefs[desc.Digest] = struct{}{}
							esgzRefsMu.Unlock()
						}
					}
					isLazy, err := r.isLazy(egctx)
					require.NoError(t, err)
					needs, err := needsConversion(desc.MediaType, compressionType)
					require.NoError(t, err)
					if needs {
						require.False(t, isLazy, "layer %q requires conversion so it must be unlazied", desc.Digest)
					}
					bDesc, err := r.getCompressionBlob(egctx, compressionType)
					if isLazy {
						require.Error(t, err)
					} else {
						require.NoError(t, err)
						checkDescriptor(ctx, t, co.cs, bDesc, compressionType)
						require.Equal(t, desc.Digest, bDesc.Digest)
					}
				}
				return nil
			})
		}
	}
	require.NoError(t, eg.Wait())

	for dgst := range esgzRefs {
		gzipDgst, ok := esgz2gzip[dgst]
		require.True(t, ok, "match for gzip blob: %s", dgst)
		delete(expectedContent, gzipDgst) // esgz blob is reused also as gzip. duplicated gzip blob is unexpected.
	}

	// verify there's a 1-to-1 mapping between the content store and what we expected to be there
	err = co.cs.Walk(ctx, func(info content.Info) error {
		dgst := info.Digest
		var matched bool
		for expected := range expectedContent {
			if dgst == expected {
				delete(expectedContent, expected)
				matched = true
				break
			}
		}
		require.True(t, matched, "unexpected blob: %s", info.Digest)
		checkInfo(ctx, t, co.cs, info)
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, map[digest.Digest]struct{}{}, expectedContent)
}

func checkInfo(ctx context.Context, t *testing.T, cs content.Store, info content.Info) {
	if info.Labels == nil {
		return
	}
	uncompressedDgst, ok := info.Labels[containerdUncompressed]
	if !ok {
		return
	}
	ra, err := cs.ReaderAt(ctx, ocispecs.Descriptor{Digest: info.Digest})
	require.NoError(t, err)
	defer ra.Close()
	decompressR, err := ctdcompression.DecompressStream(io.NewSectionReader(ra, 0, ra.Size()))
	require.NoError(t, err)

	diffID := digest.Canonical.Digester()
	_, err = io.Copy(diffID.Hash(), decompressR)
	require.NoError(t, err)
	require.Equal(t, diffID.Digest().String(), uncompressedDgst)
}

func checkDescriptor(ctx context.Context, t *testing.T, cs content.Store, desc ocispecs.Descriptor, compressionType compression.Type) {
	if desc.Annotations == nil {
		return
	}

	// Check annotations exist
	uncompressedDgst, ok := desc.Annotations[containerdUncompressed]
	require.True(t, ok, "uncompressed digest annotation not found: %q", desc.Digest)
	var uncompressedSize int64
	if compressionType == compression.EStargz {
		_, ok := desc.Annotations[estargz.TOCJSONDigestAnnotation]
		require.True(t, ok, "toc digest annotation not found: %q", desc.Digest)
		uncompressedSizeS, ok := desc.Annotations[estargz.StoreUncompressedSizeAnnotation]
		require.True(t, ok, "uncompressed size annotation not found: %q", desc.Digest)
		var err error
		uncompressedSize, err = strconv.ParseInt(uncompressedSizeS, 10, 64)
		require.NoError(t, err)
	}

	// Check annotation values are valid
	c := new(counter)
	ra, err := cs.ReaderAt(ctx, desc)
	if err != nil && errdefs.IsNotFound(err) {
		return // lazy layer
	}
	require.NoError(t, err)
	defer ra.Close()
	decompressR, err := ctdcompression.DecompressStream(io.NewSectionReader(ra, 0, ra.Size()))
	require.NoError(t, err)

	diffID := digest.Canonical.Digester()
	_, err = io.Copy(io.MultiWriter(diffID.Hash(), c), decompressR)
	require.NoError(t, err)
	require.Equal(t, diffID.Digest().String(), uncompressedDgst)
	if compressionType == compression.EStargz {
		require.Equal(t, c.size(), uncompressedSize)
	}
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

func esgzBlobDigest(uncompressedBlobBytes []byte) (digest.Digest, error) {
	buf := new(bytes.Buffer)
	compressorFunc, _ := writeEStargz(-1)
	w, err := compressorFunc(buf, ocispecs.MediaTypeImageLayerGzip)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(w, bytes.NewReader(uncompressedBlobBytes)); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	b := buf.Bytes()
	return digest.FromBytes(b), nil
}

func zstdBlobDigest(uncompressedBlobBytes []byte) (digest.Digest, error) {
	b := bytes.NewBuffer(nil)
	w, err := zstd.NewWriter(b)
	if err != nil {
		return "", err
	}
	if _, err := w.Write(uncompressedBlobBytes); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	return digest.FromBytes(b.Bytes()), nil
}

func checkNumBlobs(ctx context.Context, t *testing.T, cs content.Store, expected int) {
	c := 0
	err := cs.Walk(ctx, func(_ content.Info) error {
		c++
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, expected, c)
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

type bufferCloser struct {
	*bytes.Buffer
}

func (b bufferCloser) Close() error {
	return nil
}

func mapToBlob(m map[string]string, compress bool) ([]byte, ocispecs.Descriptor, error) {
	buf := bytes.NewBuffer(nil)
	sha := digest.SHA256.Digester()

	var dest io.WriteCloser = bufferCloser{buf}
	if compress {
		dest = gzip.NewWriter(buf)
	}
	tw := tar.NewWriter(io.MultiWriter(sha.Hash(), dest))

	for k, v := range m {
		if err := tw.WriteHeader(&tar.Header{
			Name: k,
			Size: int64(len(v)),
		}); err != nil {
			return nil, ocispecs.Descriptor{}, err
		}
		if _, err := tw.Write([]byte(v)); err != nil {
			return nil, ocispecs.Descriptor{}, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, ocispecs.Descriptor{}, err
	}
	if err := dest.Close(); err != nil {
		return nil, ocispecs.Descriptor{}, err
	}

	mediaType := ocispecs.MediaTypeImageLayer
	if compress {
		mediaType = ocispecs.MediaTypeImageLayerGzip
	}
	return buf.Bytes(), ocispecs.Descriptor{
		Digest:    digest.FromBytes(buf.Bytes()),
		MediaType: mediaType,
		Size:      int64(buf.Len()),
		Annotations: map[string]string{
			"containerd.io/uncompressed": sha.Digest().String(),
		},
	}, nil
}

func fileToBlob(file *os.File, compress bool) ([]byte, ocispecs.Descriptor, error) {
	buf := bytes.NewBuffer(nil)
	sha := digest.SHA256.Digester()

	var dest io.WriteCloser = bufferCloser{buf}
	if compress {
		dest = gzip.NewWriter(buf)
	}
	tw := tar.NewWriter(io.MultiWriter(sha.Hash(), dest))

	info, err := file.Stat()
	if err != nil {
		return nil, ocispecs.Descriptor{}, err
	}

	fi, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return nil, ocispecs.Descriptor{}, err
	}
	fi.Format = tar.FormatPAX
	fi.ModTime = fi.ModTime.Truncate(time.Second)
	fi.AccessTime = time.Time{}
	fi.ChangeTime = time.Time{}

	if err := tw.WriteHeader(fi); err != nil {
		return nil, ocispecs.Descriptor{}, err
	}
	if _, err := io.Copy(tw, file); err != nil {
		return nil, ocispecs.Descriptor{}, err
	}

	if err := tw.Close(); err != nil {
		return nil, ocispecs.Descriptor{}, err
	}
	if err := dest.Close(); err != nil {
		return nil, ocispecs.Descriptor{}, err
	}

	mediaType := ocispecs.MediaTypeImageLayer
	if compress {
		mediaType = ocispecs.MediaTypeImageLayerGzip
	}
	return buf.Bytes(), ocispecs.Descriptor{
		Digest:    digest.FromBytes(buf.Bytes()),
		MediaType: mediaType,
		Size:      int64(buf.Len()),
		Annotations: map[string]string{
			"containerd.io/uncompressed": sha.Digest().String(),
		},
	}, nil
}
