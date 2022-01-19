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
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
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
	"github.com/containerd/containerd/mount"
	"github.com/containerd/containerd/namespaces"
	"github.com/containerd/containerd/snapshots"
	"github.com/containerd/containerd/snapshots/native"
	"github.com/containerd/continuity/fs/fstest"
	"github.com/containerd/stargz-snapshotter/estargz"
	"github.com/klauspost/compress/zstd"
	"github.com/moby/buildkit/cache/metadata"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/snapshot"
	containerdsnapshot "github.com/moby/buildkit/snapshot/containerd"
	"github.com/moby/buildkit/solver"
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

	c := mdb.ContentStore()
	store = containerdsnapshot.NewContentStore(c, ns)
	lm := leaseutil.WithNamespace(ctdmetadata.NewLeaseManager(mdb), ns)
	applier := winlayers.NewFileSystemApplierWithWindows(store, apply.NewFileSystemApplier(store))
	differ := winlayers.NewWalkingDiffWithWindows(store, walking.NewWalkingDiff(store))

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
		Applier:        applier,
		Differ:         differ,
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

	err = snap.Finalize(ctx)
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

func TestMergeBlobchainID(t *testing.T) {
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

	// create a merge ref that has 3 inputs, with each input being a 3 layer blob chain
	var mergeInputs []ImmutableRef
	var descs []ocispecs.Descriptor
	descHandlers := DescHandlers(map[digest.Digest]*DescHandler{})
	for i := 0; i < 3; i++ {
		contentBuffer := contentutil.NewBuffer()
		var curBlob ImmutableRef
		for j := 0; j < 3; j++ {
			blobBytes, desc, err := mapToBlob(map[string]string{strconv.Itoa(i): strconv.Itoa(j)}, true)
			require.NoError(t, err)
			cw, err := contentBuffer.Writer(ctx)
			require.NoError(t, err)
			_, err = cw.Write(blobBytes)
			require.NoError(t, err)
			err = cw.Commit(ctx, 0, cw.Digest())
			require.NoError(t, err)
			descHandlers[desc.Digest] = &DescHandler{
				Provider: func(_ session.Group) content.Provider { return contentBuffer },
			}
			curBlob, err = cm.GetByBlob(ctx, desc, curBlob, descHandlers)
			require.NoError(t, err)
			descs = append(descs, desc)
		}
		mergeInputs = append(mergeInputs, curBlob.Clone())
	}

	mergeRef, err := cm.Merge(ctx, mergeInputs)
	require.NoError(t, err)

	_, err = mergeRef.GetRemotes(ctx, true, solver.CompressionOpt{Type: compression.Default}, false, nil)
	require.NoError(t, err)

	// verify the merge blobchain ID isn't just set to one of the inputs (regression test)
	mergeBlobchainID := mergeRef.(*immutableRef).getBlobChainID()
	for _, mergeInput := range mergeInputs {
		inputBlobchainID := mergeInput.(*immutableRef).getBlobChainID()
		require.NotEqual(t, mergeBlobchainID, inputBlobchainID)
	}

	// verify you get the merge ref when asking for an equivalent blob chain
	var curBlob ImmutableRef
	for _, desc := range descs[:len(descs)-1] {
		curBlob, err = cm.GetByBlob(ctx, desc, curBlob, descHandlers)
		require.NoError(t, err)
	}
	blobRef, err := cm.GetByBlob(ctx, descs[len(descs)-1], curBlob, descHandlers)
	require.NoError(t, err)
	require.Equal(t, mergeRef.ID(), blobRef.ID())
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
	err = snap.(*immutableRef).computeChainMetadata(leaseCtx, map[string]struct{}{snap.ID(): {}})
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
	err = snap.(*immutableRef).computeChainMetadata(ctx, map[string]struct{}{snap.ID(): {}})
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
	err = snap2.(*immutableRef).computeChainMetadata(ctx, map[string]struct{}{snap.ID(): {}, snap2.ID(): {}})
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
	err = snap.Finalize(ctx)
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

	err = snap2.Finalize(ctx)
	require.NoError(t, err)

	err = snap2.Release(ctx)
	require.NoError(t, err)

	_, err = cm.GetMutable(ctx, active.ID())
	require.Error(t, err)
	require.Equal(t, true, errors.Is(err, errNotFound))
}

func TestConversion(t *testing.T) {
	t.Parallel()
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
	store := co.cs

	// Preapre the original tar blob using archive/tar and tar command on the system
	m := map[string]string{"foo1": "bar1", "foo2": "bar2"}

	orgBlobBytesGo, orgDescGo, err := mapToBlob(m, false)
	require.NoError(t, err)
	cw, err := store.Writer(ctx, content.WithRef(fmt.Sprintf("write-test-blob-%s", orgDescGo.Digest)))
	require.NoError(t, err)
	_, err = cw.Write(orgBlobBytesGo)
	require.NoError(t, err)
	err = cw.Commit(ctx, 0, cw.Digest())
	require.NoError(t, err)

	orgBlobBytesSys, orgDescSys, err := mapToSystemTarBlob(m)
	require.NoError(t, err)
	cw, err = store.Writer(ctx, content.WithRef(fmt.Sprintf("write-test-blob-%s", orgDescSys.Digest)))
	require.NoError(t, err)
	_, err = cw.Write(orgBlobBytesSys)
	require.NoError(t, err)
	err = cw.Commit(ctx, 0, cw.Digest())
	require.NoError(t, err)

	// Tests all combination of the conversions from type i to type j preserve
	// the uncompressed digest.
	allCompression := []compression.Type{compression.Uncompressed, compression.Gzip, compression.EStargz, compression.Zstd}
	eg, egctx := errgroup.WithContext(ctx)
	for _, orgDesc := range []ocispecs.Descriptor{orgDescGo, orgDescSys} {
		for _, i := range allCompression {
			for _, j := range allCompression {
				i, j, orgDesc := i, j, orgDesc
				eg.Go(func() error {
					testName := fmt.Sprintf("%s=>%s", i, j)

					// Prepare the source compression type
					convertFunc, err := getConverter(egctx, store, orgDesc, i)
					require.NoError(t, err, testName)
					srcDesc := &orgDesc
					if convertFunc != nil {
						srcDesc, err = convertFunc(egctx, store, orgDesc)
						require.NoError(t, err, testName)
					}

					// Convert the blob
					convertFunc, err = getConverter(egctx, store, *srcDesc, j)
					require.NoError(t, err, testName)
					resDesc := srcDesc
					if convertFunc != nil {
						resDesc, err = convertFunc(egctx, store, *srcDesc)
						require.NoError(t, err, testName)
					}

					// Check the uncompressed digest is the same as the original
					convertFunc, err = getConverter(egctx, store, *resDesc, compression.Uncompressed)
					require.NoError(t, err, testName)
					recreatedDesc := resDesc
					if convertFunc != nil {
						recreatedDesc, err = convertFunc(egctx, store, *resDesc)
						require.NoError(t, err, testName)
					}
					require.Equal(t, recreatedDesc.Digest, orgDesc.Digest, testName)
					require.NotNil(t, recreatedDesc.Annotations)
					require.Equal(t, recreatedDesc.Annotations["containerd.io/uncompressed"], orgDesc.Digest.String(), testName)
					return nil
				})
			}
		}
	}
	require.NoError(t, eg.Wait())
}

type idxToVariants []map[compression.Type]ocispecs.Descriptor

func TestGetRemotes(t *testing.T) {
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
		expectedContent[esgzDgst] = struct{}{}

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
				expectedContent[esgzDgst] = struct{}{}

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

	variantsMap := make(map[string]idxToVariants)
	var variantsMapMu sync.Mutex

	// Call GetRemotes on all the refs
	eg, egctx := errgroup.WithContext(ctx)
	for _, ir := range refs {
		ir := ir.(*immutableRef)
		for _, compressionType := range []compression.Type{compression.Uncompressed, compression.Gzip, compression.EStargz, compression.Zstd} {
			compressionType := compressionType
			compressionopt := solver.CompressionOpt{
				Type:  compressionType,
				Force: true,
			}
			eg.Go(func() error {
				remotes, err := ir.GetRemotes(egctx, true, compressionopt, false, nil)
				require.NoError(t, err)
				require.Equal(t, 1, len(remotes))
				remote := remotes[0]
				refChain := ir.layerChain()
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

					variantsMapMu.Lock()
					if len(variantsMap[ir.ID()]) == 0 {
						variantsMap[ir.ID()] = make(idxToVariants, len(remote.Descriptors))
					}
					variantsMapMu.Unlock()

					require.Equal(t, len(remote.Descriptors), len(variantsMap[ir.ID()]))

					variantsMapMu.Lock()
					if variantsMap[ir.ID()][i] == nil {
						variantsMap[ir.ID()][i] = make(map[compression.Type]ocispecs.Descriptor)
					}
					variantsMap[ir.ID()][i][compressionType] = desc
					variantsMapMu.Unlock()

					r := refChain[i]
					isLazy, err := r.isLazy(egctx)
					require.NoError(t, err)
					needs, err := needsConversion(ctx, co.cs, desc, compressionType)
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

	// Check if "all" option returns all available blobs
	for _, ir := range refs {
		ir := ir.(*immutableRef)
		variantsMapMu.Lock()
		variants, ok := variantsMap[ir.ID()]
		variantsMapMu.Unlock()
		require.True(t, ok, ir.ID())
		for _, compressionType := range []compression.Type{compression.Uncompressed, compression.Gzip, compression.EStargz, compression.Zstd} {
			compressionType := compressionType
			compressionopt := solver.CompressionOpt{Type: compressionType}
			eg.Go(func() error {
				remotes, err := ir.GetRemotes(egctx, false, compressionopt, true, nil)
				require.NoError(t, err)
				require.True(t, len(remotes) > 0, "for %s : %d", compressionType, len(remotes))
				gotMain, gotVariants := remotes[0], remotes[1:]

				// Check the main blob is compatible with all == false
				mainOnly, err := ir.GetRemotes(egctx, false, compressionopt, false, nil)
				require.NoError(t, err)
				require.Equal(t, 1, len(mainOnly))
				mainRemote := mainOnly[0]
				require.Equal(t, len(mainRemote.Descriptors), len(gotMain.Descriptors))
				for i := 0; i < len(mainRemote.Descriptors); i++ {
					require.Equal(t, mainRemote.Descriptors[i].Digest, gotMain.Descriptors[i].Digest)
				}

				// Check all variants are covered
				checkVariantsCoverage(egctx, t, variants, len(remotes[0].Descriptors)-1, gotVariants, &compressionType)
				return nil
			})
		}
	}
	require.NoError(t, eg.Wait())
}

func checkVariantsCoverage(ctx context.Context, t *testing.T, variants idxToVariants, idx int, remotes []*solver.Remote, expectCompression *compression.Type) {
	if idx < 0 {
		for _, r := range remotes {
			require.Equal(t, len(r.Descriptors), 0)
		}
		return
	}

	// check the contents of the topmost blob of each remote
	got := make(map[digest.Digest][]*solver.Remote)
	for _, r := range remotes {
		require.Equal(t, len(r.Descriptors)-1, idx, "idx = %d", idx)

		// record this variant
		topmost, lower := r.Descriptors[idx], r.Descriptors[:idx]
		got[topmost.Digest] = append(got[topmost.Digest], &solver.Remote{Descriptors: lower, Provider: r.Provider})

		// check the contents
		r, err := r.Provider.ReaderAt(ctx, topmost)
		require.NoError(t, err)
		dgstr := digest.Canonical.Digester()
		_, err = io.Copy(dgstr.Hash(), io.NewSectionReader(r, 0, topmost.Size))
		require.NoError(t, err)
		require.NoError(t, r.Close())
		require.Equal(t, dgstr.Digest(), topmost.Digest)
	}

	// check the lowers as well
	eg, egctx := errgroup.WithContext(ctx)
	for _, lowers := range got {
		lowers := lowers
		eg.Go(func() error {
			checkVariantsCoverage(egctx, t, variants, idx-1, lowers, nil) // expect all compression variants
			return nil
		})
	}
	require.NoError(t, eg.Wait())

	// check the coverage of the variants
	targets := variants[idx]
	if expectCompression != nil {
		c, ok := variants[idx][*expectCompression]
		require.True(t, ok, "idx = %d, compression = %q, variants = %+v, got = %+v", idx, *expectCompression, variants[idx], got)
		targets = map[compression.Type]ocispecs.Descriptor{*expectCompression: c}
	}
	for c, d := range targets {
		_, ok := got[d.Digest]
		require.True(t, ok, "idx = %d, compression = %q, want = %+v, got = %+v", idx, c, d, got)
		delete(got, d.Digest)
	}
	require.Equal(t, 0, len(got))
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

func TestMergeOp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Depends on unimplemented merge-op support on Windows")
	}

	// This just tests the basic Merge method and some of the logic with releasing merge refs.
	// Tests for the fs merge logic are in client_test and snapshotter_test.
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

	emptyMerge, err := cm.Merge(ctx, nil)
	require.NoError(t, err)
	require.Nil(t, emptyMerge)

	var baseRefs []ImmutableRef
	for i := 0; i < 6; i++ {
		active, err := cm.New(ctx, nil, nil)
		require.NoError(t, err)
		m, err := active.Mount(ctx, false, nil)
		require.NoError(t, err)
		lm := snapshot.LocalMounter(m)
		target, err := lm.Mount()
		require.NoError(t, err)
		err = fstest.Apply(
			fstest.CreateFile(strconv.Itoa(i), []byte(strconv.Itoa(i)), 0777),
		).Apply(target)
		require.NoError(t, err)
		err = lm.Unmount()
		require.NoError(t, err)
		snap, err := active.Commit(ctx)
		require.NoError(t, err)
		baseRefs = append(baseRefs, snap)
		size, err := snap.(*immutableRef).size(ctx)
		require.NoError(t, err)
		require.EqualValues(t, 8192, size)
	}

	singleMerge, err := cm.Merge(ctx, baseRefs[:1])
	require.NoError(t, err)
	m, err := singleMerge.Mount(ctx, true, nil)
	require.NoError(t, err)
	ms, unmount, err := m.Mount()
	require.NoError(t, err)
	require.Len(t, ms, 1)
	require.Equal(t, ms[0].Type, "bind")
	err = fstest.CheckDirectoryEqualWithApplier(ms[0].Source, fstest.Apply(
		fstest.CreateFile(strconv.Itoa(0), []byte(strconv.Itoa(0)), 0777),
	))
	require.NoError(t, err)
	require.NoError(t, unmount())
	require.NoError(t, singleMerge.Release(ctx))

	err = cm.Prune(ctx, nil, client.PruneInfo{Filter: []string{
		"id==" + singleMerge.ID(),
	}})
	require.NoError(t, err)

	merge1, err := cm.Merge(ctx, baseRefs[:3])
	require.NoError(t, err)
	_, err = merge1.Mount(ctx, true, nil)
	require.NoError(t, err)
	size1, err := merge1.(*immutableRef).size(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 4096, size1) // hardlinking means all but the first snapshot doesn't take up space
	checkDiskUsage(ctx, t, cm, 7, 0)

	merge2, err := cm.Merge(ctx, baseRefs[3:])
	require.NoError(t, err)
	_, err = merge2.Mount(ctx, true, nil)
	require.NoError(t, err)
	size2, err := merge2.(*immutableRef).size(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 4096, size2)
	checkDiskUsage(ctx, t, cm, 8, 0)

	for _, ref := range baseRefs {
		require.NoError(t, ref.Release(ctx))
	}
	checkDiskUsage(ctx, t, cm, 8, 0)
	// should still be able to use merges based on released refs

	merge3, err := cm.Merge(ctx, []ImmutableRef{merge1, merge2})
	require.NoError(t, err)
	require.NoError(t, merge1.Release(ctx))
	require.NoError(t, merge2.Release(ctx))
	_, err = merge3.Mount(ctx, true, nil)
	require.NoError(t, err)
	size3, err := merge3.(*immutableRef).size(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 4096, size3)
	require.Len(t, merge3.(*immutableRef).mergeParents, 6)
	checkDiskUsage(ctx, t, cm, 7, 2)

	require.NoError(t, merge3.Release(ctx))
	checkDiskUsage(ctx, t, cm, 0, 9)
	err = cm.Prune(ctx, nil, client.PruneInfo{All: true})
	require.NoError(t, err)
	checkDiskUsage(ctx, t, cm, 0, 0)
}

func TestLoadHalfFinalizedRef(t *testing.T) {
	// This test simulates the situation where a ref w/ an equalMutable has its
	// snapshot committed but there is a crash before the metadata is updated to
	// clear the equalMutable field. It's expected that the mutable will be
	// removed and the immutable ref will continue to be usable.
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
	defer cleanup()
	cm := co.manager.(*cacheManager)

	mref, err := cm.New(ctx, nil, nil, CachePolicyRetain)
	require.NoError(t, err)
	mutRef := mref.(*mutableRef)

	iref, err := mutRef.Commit(ctx)
	require.NoError(t, err)
	immutRef := iref.(*immutableRef)

	require.NoError(t, mref.Release(ctx))

	_, err = co.lm.Create(ctx, func(l *leases.Lease) error {
		l.ID = immutRef.ID()
		l.Labels = map[string]string{
			"containerd.io/gc.flat": time.Now().UTC().Format(time.RFC3339Nano),
		}
		return nil
	})
	require.NoError(t, err)
	err = co.lm.AddResource(ctx, leases.Lease{ID: immutRef.ID()}, leases.Resource{
		ID:   immutRef.getSnapshotID(),
		Type: "snapshots/" + cm.Snapshotter.Name(),
	})
	require.NoError(t, err)

	err = cm.Snapshotter.Commit(ctx, immutRef.getSnapshotID(), mutRef.getSnapshotID())
	require.NoError(t, err)

	_, err = cm.Snapshotter.Stat(ctx, mutRef.getSnapshotID())
	require.Error(t, err)

	require.NoError(t, iref.Release(ctx))

	require.NoError(t, cm.Close())
	require.NoError(t, cleanup())

	co, cleanup, err = newCacheManager(ctx, cmOpt{
		tmpdir:          tmpdir,
		snapshotter:     snapshotter,
		snapshotterName: "native",
	})
	require.NoError(t, err)
	defer cleanup()
	cm = co.manager.(*cacheManager)

	_, err = cm.GetMutable(ctx, mutRef.ID())
	require.ErrorIs(t, err, errNotFound)

	iref, err = cm.Get(ctx, immutRef.ID())
	require.NoError(t, err)
	require.NoError(t, iref.Finalize(ctx))
	immutRef = iref.(*immutableRef)

	_, err = cm.Snapshotter.Stat(ctx, immutRef.getSnapshotID())
	require.NoError(t, err)
}

func TestMountReadOnly(t *testing.T) {
	t.Parallel()
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
		snapshotterName: "overlay",
	})
	require.NoError(t, err)
	defer cleanup()
	cm := co.manager

	mutRef, err := cm.New(ctx, nil, nil)
	require.NoError(t, err)

	for i := 0; i < 2; i++ {
		rwMntable, err := mutRef.Mount(ctx, false, nil)
		require.NoError(t, err)
		rwMnts, release, err := rwMntable.Mount()
		require.NoError(t, err)
		defer release()
		require.Len(t, rwMnts, 1)
		require.False(t, isReadOnly(rwMnts[0]))

		roMntable, err := mutRef.Mount(ctx, true, nil)
		require.NoError(t, err)
		roMnts, release, err := roMntable.Mount()
		require.NoError(t, err)
		defer release()
		require.Len(t, roMnts, 1)
		require.True(t, isReadOnly(roMnts[0]))

		immutRef, err := mutRef.Commit(ctx)
		require.NoError(t, err)

		roMntable, err = immutRef.Mount(ctx, true, nil)
		require.NoError(t, err)
		roMnts, release, err = roMntable.Mount()
		require.NoError(t, err)
		defer release()
		require.Len(t, roMnts, 1)
		require.True(t, isReadOnly(roMnts[0]))

		rwMntable, err = immutRef.Mount(ctx, false, nil)
		require.NoError(t, err)
		rwMnts, release, err = rwMntable.Mount()
		require.NoError(t, err)
		defer release()
		require.Len(t, rwMnts, 1)
		// once immutable, even when readonly=false, the mount is still readonly
		require.True(t, isReadOnly(rwMnts[0]))

		// repeat with a ref that has a parent
		mutRef, err = cm.New(ctx, immutRef, nil)
		require.NoError(t, err)
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

func esgzBlobDigest(uncompressedBlobBytes []byte) (blobDigest digest.Digest, err error) {
	esgzDigester := digest.Canonical.Digester()
	w := estargz.NewWriter(esgzDigester.Hash())
	if err := w.AppendTarLossLess(bytes.NewReader(uncompressedBlobBytes)); err != nil {
		return "", err
	}
	if _, err := w.Close(); err != nil {
		return "", err
	}
	return esgzDigester.Digest(), nil
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

func mapToSystemTarBlob(m map[string]string) ([]byte, ocispecs.Descriptor, error) {
	tmpdir, err := ioutil.TempDir("", "tarcreation")
	if err != nil {
		return nil, ocispecs.Descriptor{}, err
	}
	defer os.RemoveAll(tmpdir)

	expected := map[string]string{}
	for k, v := range m {
		expected[k] = v
		if err := ioutil.WriteFile(filepath.Join(tmpdir, k), []byte(v), 0600); err != nil {
			return nil, ocispecs.Descriptor{}, err
		}
	}

	cmd := exec.Command("tar", "-C", tmpdir, "-c", ".")
	tarout, err := cmd.Output()
	if err != nil {
		return nil, ocispecs.Descriptor{}, err
	}

	tr := tar.NewReader(bytes.NewReader(tarout))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, ocispecs.Descriptor{}, err
		}
		k := strings.TrimPrefix(filepath.Clean("/"+h.Name), "/")
		if k == "" {
			continue // ignore the root entry
		}
		v, ok := expected[k]
		if !ok {
			return nil, ocispecs.Descriptor{}, errors.Errorf("unexpected file %s", h.Name)
		}
		delete(expected, k)
		gotV, err := ioutil.ReadAll(tr)
		if err != nil {
			return nil, ocispecs.Descriptor{}, err
		}
		if string(gotV) != string(v) {
			return nil, ocispecs.Descriptor{}, errors.Errorf("unexpected contents of %s", h.Name)
		}
	}
	if len(expected) > 0 {
		return nil, ocispecs.Descriptor{}, errors.Errorf("expected file doesn't archived: %+v", expected)
	}

	return tarout, ocispecs.Descriptor{
		Digest:    digest.FromBytes(tarout),
		MediaType: ocispecs.MediaTypeImageLayer,
		Size:      int64(len(tarout)),
		Annotations: map[string]string{
			"containerd.io/uncompressed": digest.FromBytes(tarout).String(),
		},
	}, nil
}

func isReadOnly(mnt mount.Mount) bool {
	var hasUpperdir bool
	for _, o := range mnt.Options {
		if o == "ro" {
			return true
		} else if strings.HasPrefix(o, "upperdir=") {
			hasUpperdir = true
		}
	}
	if mnt.Type == "overlay" {
		return !hasUpperdir
	}
	return false
}
