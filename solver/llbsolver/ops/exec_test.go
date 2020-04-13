package ops

import (
	"context"
	"io/ioutil"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/moby/buildkit/util/testutil"

	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/content/local"
	"github.com/containerd/containerd/diff/apply"
	"github.com/containerd/containerd/leases"
	ctdmetadata "github.com/containerd/containerd/metadata"
	"github.com/containerd/containerd/namespaces"
	"github.com/containerd/containerd/snapshots"
	"github.com/containerd/containerd/snapshots/native"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/cache/metadata"
	"github.com/moby/buildkit/snapshot"
	containerdsnapshot "github.com/moby/buildkit/snapshot/containerd"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/leaseutil"
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
	manager cache.Manager
	lm      leases.Manager
	cs      content.Store
	md      *metadata.Store
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

	lm := ctdmetadata.NewLeaseManager(mdb)

	cm, err := cache.NewManager(cache.ManagerOpt{
		Snapshotter:    snapshot.FromContainerdSnapshotter(opt.snapshotterName, containerdsnapshot.NSSnapshotter(ns, mdb.Snapshotter(opt.snapshotterName)), nil),
		MetadataStore:  md,
		ContentStore:   mdb.ContentStore(),
		LeaseManager:   leaseutil.WithNamespace(lm, ns),
		GarbageCollect: mdb.GarbageCollect,
		Applier:        apply.NewFileSystemApplier(mdb.ContentStore()),
	})
	if err != nil {
		return nil, nil, err
	}
	return &cmOut{
		manager: cm,
		lm:      lm,
		cs:      mdb.ContentStore(),
		md:      md,
	}, cleanup, nil
}

func TestDedupPaths(t *testing.T) {
	res := dedupePaths([]string{"Gemfile", "Gemfile/foo"})
	require.Equal(t, []string{"Gemfile"}, res)

	res = dedupePaths([]string{"Gemfile/bar", "Gemfile/foo"})
	require.Equal(t, []string{"Gemfile/bar", "Gemfile/foo"}, res)

	res = dedupePaths([]string{"Gemfile", "Gemfile.lock"})
	require.Equal(t, []string{"Gemfile", "Gemfile.lock"}, res)

	res = dedupePaths([]string{"Gemfile.lock", "Gemfile"})
	require.Equal(t, []string{"Gemfile", "Gemfile.lock"}, res)

	res = dedupePaths([]string{"foo", "Gemfile", "Gemfile/foo"})
	require.Equal(t, []string{"Gemfile", "foo"}, res)

	res = dedupePaths([]string{"foo/bar/baz", "foo/bara", "foo/bar/bax", "foo/bar"})
	require.Equal(t, []string{"foo/bar", "foo/bara"}, res)
}

func newRefGetter(m cache.Manager, md *metadata.Store, shared *cacheRefs) *cacheRefGetter {
	return &cacheRefGetter{
		locker:          &sync.Mutex{},
		cacheMounts:     map[string]*cacheRefShare{},
		cm:              m,
		md:              md,
		globalCacheRefs: shared,
	}
}

func TestCacheMountPrivateRefs(t *testing.T) {
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

	g1 := newRefGetter(co.manager, co.md, sharedCacheRefs)
	g2 := newRefGetter(co.manager, co.md, sharedCacheRefs)
	g3 := newRefGetter(co.manager, co.md, sharedCacheRefs)
	g4 := newRefGetter(co.manager, co.md, sharedCacheRefs)

	ref, err := g1.getRefCacheDir(ctx, nil, "foo", pb.CacheSharingOpt_PRIVATE)
	require.NoError(t, err)

	ref2, err := g1.getRefCacheDir(ctx, nil, "bar", pb.CacheSharingOpt_PRIVATE)
	require.NoError(t, err)

	// different ID returns different ref
	require.NotEqual(t, ref.ID(), ref2.ID())

	// same ID on same mount still shares the reference
	ref3, err := g1.getRefCacheDir(ctx, nil, "foo", pb.CacheSharingOpt_PRIVATE)
	require.NoError(t, err)

	require.Equal(t, ref.ID(), ref3.ID())

	// same ID on different mount gets a new ID
	ref4, err := g2.getRefCacheDir(ctx, nil, "foo", pb.CacheSharingOpt_PRIVATE)
	require.NoError(t, err)

	require.NotEqual(t, ref.ID(), ref4.ID())

	// releasing one of two refs still keeps first ID private
	ref.Release(testutil.GetContext(t))

	ref5, err := g3.getRefCacheDir(ctx, nil, "foo", pb.CacheSharingOpt_PRIVATE)
	require.NoError(t, err)
	require.NotEqual(t, ref.ID(), ref5.ID())
	require.NotEqual(t, ref4.ID(), ref5.ID())

	// releasing all refs releases ID to be reused
	ref3.Release(testutil.GetContext(t))

	ref5, err = g4.getRefCacheDir(ctx, nil, "foo", pb.CacheSharingOpt_PRIVATE)
	require.NoError(t, err)

	require.Equal(t, ref.ID(), ref5.ID())

	// other mounts still keep their IDs
	ref6, err := g2.getRefCacheDir(ctx, nil, "foo", pb.CacheSharingOpt_PRIVATE)
	require.NoError(t, err)
	require.Equal(t, ref4.ID(), ref6.ID())
}

func TestCacheMountSharedRefs(t *testing.T) {
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

	g1 := newRefGetter(co.manager, co.md, sharedCacheRefs)
	g2 := newRefGetter(co.manager, co.md, sharedCacheRefs)
	g3 := newRefGetter(co.manager, co.md, sharedCacheRefs)

	ref, err := g1.getRefCacheDir(ctx, nil, "foo", pb.CacheSharingOpt_SHARED)
	require.NoError(t, err)

	ref2, err := g1.getRefCacheDir(ctx, nil, "bar", pb.CacheSharingOpt_SHARED)
	require.NoError(t, err)

	// different ID returns different ref
	require.NotEqual(t, ref.ID(), ref2.ID())

	// same ID on same mount still shares the reference
	ref3, err := g1.getRefCacheDir(ctx, nil, "foo", pb.CacheSharingOpt_SHARED)
	require.NoError(t, err)

	require.Equal(t, ref.ID(), ref3.ID())

	// same ID on different mount gets same ID
	ref4, err := g2.getRefCacheDir(ctx, nil, "foo", pb.CacheSharingOpt_SHARED)
	require.NoError(t, err)

	require.Equal(t, ref.ID(), ref4.ID())

	// private gets a new ID
	ref5, err := g3.getRefCacheDir(ctx, nil, "foo", pb.CacheSharingOpt_PRIVATE)
	require.NoError(t, err)
	require.NotEqual(t, ref.ID(), ref5.ID())
}

func TestCacheMountLockedRefs(t *testing.T) {
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

	g1 := newRefGetter(co.manager, co.md, sharedCacheRefs)
	g2 := newRefGetter(co.manager, co.md, sharedCacheRefs)

	ref, err := g1.getRefCacheDir(ctx, nil, "foo", pb.CacheSharingOpt_LOCKED)
	require.NoError(t, err)

	ref2, err := g1.getRefCacheDir(ctx, nil, "bar", pb.CacheSharingOpt_LOCKED)
	require.NoError(t, err)

	// different ID returns different ref
	require.NotEqual(t, ref.ID(), ref2.ID())

	// same ID on same mount still shares the reference
	ref3, err := g1.getRefCacheDir(ctx, nil, "foo", pb.CacheSharingOpt_LOCKED)
	require.NoError(t, err)

	require.Equal(t, ref.ID(), ref3.ID())

	// same ID on different mount blocks
	gotRef4 := make(chan struct{})
	go func() {
		ref4, err := g2.getRefCacheDir(ctx, nil, "foo", pb.CacheSharingOpt_LOCKED)
		require.NoError(t, err)
		require.Equal(t, ref.ID(), ref4.ID())
		close(gotRef4)
	}()

	select {
	case <-gotRef4:
		require.FailNow(t, "mount did not lock")
	case <-time.After(500 * time.Millisecond):
	}

	ref.Release(ctx)
	ref3.Release(ctx)

	select {
	case <-gotRef4:
	case <-time.After(500 * time.Millisecond):
		require.FailNow(t, "mount did not unlock")
	}
}

// moby/buildkit#1322
func TestCacheMountSharedRefsDeadlock(t *testing.T) {
	// not parallel
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

	var sharedCacheRefs = &cacheRefs{}

	g1 := newRefGetter(co.manager, co.md, sharedCacheRefs)
	g2 := newRefGetter(co.manager, co.md, sharedCacheRefs)

	ref, err := g1.getRefCacheDir(ctx, nil, "foo", pb.CacheSharingOpt_SHARED)
	require.NoError(t, err)

	cacheRefReleaseHijack = func() {
		time.Sleep(200 * time.Millisecond)
	}
	cacheRefCloneHijack = func() {
		time.Sleep(400 * time.Millisecond)
	}
	defer func() {
		cacheRefReleaseHijack = nil
		cacheRefCloneHijack = nil
	}()
	eg, _ := errgroup.WithContext(testutil.GetContext(t))

	eg.Go(func() error {
		return ref.Release(testutil.GetContext(t))
	})
	eg.Go(func() error {
		_, err := g2.getRefCacheDir(ctx, nil, "foo", pb.CacheSharingOpt_SHARED)
		return err
	})

	done := make(chan struct{})
	go func() {
		err = eg.Wait()
		require.NoError(t, err)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		require.FailNow(t, "deadlock on releasing while getting new ref")
	}
}
