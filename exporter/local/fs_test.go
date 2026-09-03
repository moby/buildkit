package local

import (
	"context"
	iofs "io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/containerd/containerd/content/local"
	"github.com/containerd/containerd/diff/apply"
	"github.com/containerd/containerd/diff/walking"
	ctdmetadata "github.com/containerd/containerd/metadata"
	"github.com/containerd/containerd/namespaces"
	"github.com/containerd/containerd/snapshots"
	"github.com/containerd/containerd/snapshots/native"
	"github.com/docker/docker/pkg/idtools"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/cache/metadata"
	"github.com/moby/buildkit/snapshot"
	containerdsnapshot "github.com/moby/buildkit/snapshot/containerd"
	"github.com/moby/buildkit/util/leaseutil"
	"github.com/moby/buildkit/util/winlayers"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	bolt "go.etcd.io/bbolt"
)

func TestCreateFS_DirMissingFromIDMap(t *testing.T) {
	if runtime.GOOS == "windows" {
		// The chown doesn't work quite the same on Windows, so skip this test.
		t.Skip("Skipping test on Windows")
	}

	idmap := &idtools.IdentityMapping{
		UIDMaps: []idtools.IDMap{
			{ContainerID: 0, HostID: 0, Size: 1},
		},
		GIDMaps: []idtools.IDMap{
			{ContainerID: 0, HostID: 0, Size: 1},
		},
	}

	ctx := namespaces.WithNamespace(context.Background(), "buildkit-test")
	cm, err := newCacheManager(ctx, t, idmap)
	require.NoError(t, err)
	sessionID := "sessionID"

	active, err := cm.New(ctx, nil, nil, cache.CachePolicyRetain)
	require.NoError(t, err)

	m, err := active.Mount(ctx, false, nil)
	require.NoError(t, err)

	lm := snapshot.LocalMounter(m)
	target, err := lm.Mount()
	require.NoError(t, err)

	_ = os.MkdirAll(filepath.Join(target, "dir"), 0755)

	f, err := os.Create(filepath.Join(target, "dir", "test.txt"))
	require.NoError(t, err)
	_ = f.Close()

	_ = os.MkdirAll(filepath.Join(target, "unmapped-dir"), 0755)
	os.Chown(filepath.Join(target, "unmapped-dir"), 1001, 1001)

	f, err = os.Create(filepath.Join(target, "unmapped-dir", "test.txt"))
	require.NoError(t, err)
	_ = f.Close()

	lm.Unmount()

	snap, err := active.Commit(ctx)
	require.NoError(t, err)

	activeRef, err := cm.Get(ctx, snap.ID(), nil)
	require.NoError(t, err)

	fs, _, err := CreateFS(ctx, sessionID, "k???", activeRef, nil, time.Now(), CreateFSOpts{})
	require.NoError(t, err)
	require.NotNil(t, fs)

	p := []string{}
	err = fs.Walk(context.Background(), ".", iofs.WalkDirFunc(func(path string, info iofs.DirEntry, err error) error {
		p = append(p, path)
		return nil
	}))
	require.NoError(t, err)
	require.Equal(t, []string{
		"dir",
		filepath.Join("dir", "test.txt"),
		// TODO(nicks): "unmapped-dir" should be included in the walk. It is not
		// because of an upstream bug in fsutil. See:
		// https://github.com/tonistiigi/fsutil/pull/203
		//
		// "unmapped-dir",
		filepath.Join("unmapped-dir", "test.txt"),
	}, p)
}

func newCacheManager(ctx context.Context, t *testing.T, idmap *idtools.IdentityMapping) (cm cache.Manager, err error) {
	ns, ok := namespaces.Namespace(ctx)
	if !ok {
		return nil, errors.Errorf("namespace required for test")
	}

	tmpdir := t.TempDir()

	snapshotter, err := native.NewSnapshotter(filepath.Join(tmpdir, "snapshots"))
	if err != nil {
		return nil, err
	}

	store, err := local.NewStore(tmpdir)
	if err != nil {
		return nil, err
	}

	db, err := bolt.Open(filepath.Join(tmpdir, "containerdmeta.db"), 0644, nil)
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() {
		require.NoError(t, db.Close())
	})

	sn := "native-snapshotter"
	mdb := ctdmetadata.NewDB(db, store, map[string]snapshots.Snapshotter{
		sn: snapshotter,
	})
	if err := mdb.Init(context.TODO()); err != nil {
		return nil, err
	}

	lm := leaseutil.WithNamespace(ctdmetadata.NewLeaseManager(mdb), ns)
	c := mdb.ContentStore()
	applier := winlayers.NewFileSystemApplierWithWindows(c, apply.NewFileSystemApplier(c))
	differ := winlayers.NewWalkingDiffWithWindows(c, walking.NewWalkingDiff(c))

	md, err := metadata.NewStore(filepath.Join(tmpdir, "metadata.db"))
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() {
		require.NoError(t, md.Close())
	})

	cm, err = cache.NewManager(cache.ManagerOpt{
		Snapshotter:    snapshot.FromContainerdSnapshotter(sn, containerdsnapshot.NSSnapshotter(ns, mdb.Snapshotter(sn)), idmap),
		MetadataStore:  md,
		ContentStore:   c,
		Applier:        applier,
		Differ:         differ,
		LeaseManager:   lm,
		GarbageCollect: mdb.GarbageCollect,
		MountPoolRoot:  filepath.Join(tmpdir, "cachemounts"),
	})
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() {
		require.NoError(t, cm.Close())
	})

	return cm, nil
}
