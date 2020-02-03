package ops

import (
	"context"
	"io/ioutil"
	"os"
	"path/filepath"
	"sync"
	"testing"

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

	g1 := &cacheRefGetter{
		locker:      &sync.Mutex{},
		cacheMounts: map[string]*cacheRefShare{},
		cm:          co.manager,
		md:          co.md,
	}

	g2 := &cacheRefGetter{
		locker:      g1.locker,
		cacheMounts: map[string]*cacheRefShare{},
		cm:          co.manager,
		md:          co.md,
	}

	g3 := &cacheRefGetter{
		locker:      g1.locker,
		cacheMounts: map[string]*cacheRefShare{},
		cm:          co.manager,
		md:          co.md,
	}

	g4 := &cacheRefGetter{
		locker:      g1.locker,
		cacheMounts: map[string]*cacheRefShare{},
		cm:          co.manager,
		md:          co.md,
	}

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
	ref.Release(context.TODO())

	ref5, err := g3.getRefCacheDir(ctx, nil, "foo", pb.CacheSharingOpt_PRIVATE)
	require.NoError(t, err)
	require.NotEqual(t, ref.ID(), ref5.ID())
	require.NotEqual(t, ref4.ID(), ref5.ID())

	// releasing all refs releases ID to be reused
	ref3.Release(context.TODO())

	ref5, err = g4.getRefCacheDir(ctx, nil, "foo", pb.CacheSharingOpt_PRIVATE)
	require.NoError(t, err)

	require.Equal(t, ref.ID(), ref5.ID())

	// other mounts still keep their IDs
	ref6, err := g2.getRefCacheDir(ctx, nil, "foo", pb.CacheSharingOpt_PRIVATE)
	require.NoError(t, err)
	require.Equal(t, ref4.ID(), ref6.ID())
}
