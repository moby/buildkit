package containerblob

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/containerd/containerd/v2/core/diff/apply"
	ctdmetadata "github.com/containerd/containerd/v2/core/metadata"
	"github.com/containerd/containerd/v2/core/snapshots"
	"github.com/containerd/containerd/v2/pkg/reference"
	"github.com/containerd/containerd/v2/plugins/content/local"
	"github.com/containerd/containerd/v2/plugins/diff/walking"
	"github.com/containerd/containerd/v2/plugins/snapshots/native"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/cache/metadata"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/snapshot"
	containerdsnapshot "github.com/moby/buildkit/snapshot/containerd"
	"github.com/moby/buildkit/util/contentutil"
	"github.com/moby/buildkit/util/leaseutil"
	"github.com/moby/buildkit/util/winlayers"
	digest "github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
	bolt "go.etcd.io/bbolt"
)

func TestPullerSnapshotDigestVerification(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		scheme     string
		ref        string
		blob       []byte
		stream     []byte
		algorithm  digest.Algorithm
		wantErr    string
		wantInUse  int
		wantUnused int
	}{
		{
			name:       "docker-image-blob sha256 success",
			scheme:     "docker-image+blob",
			ref:        "docker.io/library/busybox",
			blob:       []byte("sha256 docker payload"),
			stream:     []byte("sha256 docker payload"),
			algorithm:  digest.SHA256,
			wantInUse:  1,
			wantUnused: 0,
		},
		{
			name:       "docker-image-blob sha256 mismatch releases ref",
			scheme:     "docker-image+blob",
			ref:        "docker.io/library/busybox",
			blob:       []byte("sha256 docker payload"),
			stream:     []byte("sha256 docker payload modified"),
			algorithm:  digest.SHA256,
			wantErr:    "blob digest mismatch",
			wantInUse:  0,
			wantUnused: 0,
		},
		{
			name:       "oci-layout-blob sha512 success",
			scheme:     "oci-layout+blob",
			ref:        "example.com/repo/layout",
			blob:       []byte("sha512 oci payload"),
			stream:     []byte("sha512 oci payload"),
			algorithm:  digest.SHA512,
			wantInUse:  1,
			wantUnused: 0,
		},
		{
			name:       "oci-layout-blob sha512 mismatch releases ref",
			scheme:     "oci-layout+blob",
			ref:        "example.com/repo/layout",
			blob:       []byte("sha512 oci payload"),
			stream:     []byte("sha512 oci payload modified"),
			algorithm:  digest.SHA512,
			wantErr:    "blob digest mismatch",
			wantInUse:  0,
			wantUnused: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			src, cm := newTestContainerBlobSource(t)
			dgst := tc.algorithm.FromBytes(tc.blob)
			p := &puller{
				src: src,
				id: &ImageBlobIdentifier{
					Reference:  mustParseReference(t, tc.ref+"@"+dgst.String()),
					SchemeName: tc.scheme,
				},
				rc:   io.NopCloser(bytes.NewReader(tc.stream)),
				dgst: dgst,
			}

			ref, err := p.Snapshot(ctx, nil)
			if tc.wantErr != "" {
				require.Error(t, err)
				require.ErrorContains(t, err, tc.wantErr)
				require.ErrorContains(t, err, dgst.String())
				checkDiskUsageCounts(ctx, t, cm, tc.wantInUse, tc.wantUnused)
				require.NoError(t, cm.Prune(ctx, nil, client.PruneInfo{All: true}))
				checkDiskUsageCounts(ctx, t, cm, 0, 0)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, ref)
			checkDiskUsageCounts(ctx, t, cm, tc.wantInUse, tc.wantUnused)

			dt, err := readRefFile(ctx, ref, dgst.Encoded())
			require.NoError(t, err)
			require.Equal(t, tc.blob, dt)

			require.NoError(t, ref.Release(context.WithoutCancel(ctx)))
			checkDiskUsageCounts(ctx, t, cm, 0, 1)

			require.NoError(t, cm.Prune(ctx, nil, client.PruneInfo{All: true}))
			checkDiskUsageCounts(ctx, t, cm, 0, 0)
		})
	}
}

func TestPullerCacheKeyVersionInvalidatesLegacyEntries(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	src := &Source{
		SourceOpt: SourceOpt{
			ContentStore: contentutil.NewBuffer(),
		},
	}

	dgst := digest.FromString("containerblob cache key version")
	p := &puller{
		src: src,
		id: &ImageBlobIdentifier{
			Reference:  mustParseReference(t, "docker.io/library/busybox@"+dgst.String()),
			SchemeName: "docker-image+blob",
			Filename:   "payload.tgz",
			Perm:       0640,
			UID:        1000,
			GID:        1001,
		},
	}

	key, pin, _, done, err := p.CacheKey(ctx, nil, 0)
	require.NoError(t, err)
	require.True(t, done)
	require.Equal(t, dgst.String(), pin)

	legacyKey := legacyContainerBlobCacheKey(t, p.id)
	require.NotEqual(t, legacyKey, key)
}

func legacyContainerBlobCacheKey(t *testing.T, id *ImageBlobIdentifier) string {
	t.Helper()

	dt, err := json.Marshal(struct {
		Digest         digest.Digest
		Filename       string
		Perm, UID, GID int
	}{
		Digest:   id.Reference.Digest(),
		Filename: id.Filename,
		Perm:     id.Perm,
		UID:      id.UID,
		GID:      id.GID,
	})
	require.NoError(t, err)

	return digest.FromBytes(dt).String()
}

func mustParseReference(t *testing.T, ref string) reference.Spec {
	t.Helper()

	spec, err := reference.Parse(ref)
	require.NoError(t, err)
	return spec
}

func readRefFile(ctx context.Context, ref cache.ImmutableRef, filename string) ([]byte, error) {
	mountable, err := ref.Mount(ctx, true, nil)
	if err != nil {
		return nil, err
	}

	lm := snapshot.LocalMounter(mountable)
	dir, err := lm.Mount()
	if err != nil {
		return nil, err
	}
	defer lm.Unmount()

	dt, err := os.ReadFile(filepath.Join(dir, filename))
	if err != nil {
		return nil, err
	}
	return dt, nil
}

func checkDiskUsageCounts(ctx context.Context, t *testing.T, cm cache.Manager, inUse, unused int) {
	t.Helper()

	du, err := cm.DiskUsage(ctx, client.DiskUsageInfo{})
	require.NoError(t, err)

	var inUseActual, unusedActual int
	for _, usage := range du {
		if usage.InUse {
			inUseActual++
			continue
		}
		unusedActual++
	}

	require.Equal(t, inUse, inUseActual)
	require.Equal(t, unused, unusedActual)
}

func newTestContainerBlobSource(t *testing.T) (*Source, cache.Manager) {
	t.Helper()

	cm, err := newTestCacheManager(t)
	require.NoError(t, err)

	src, err := NewSource(SourceOpt{
		ContentStore:  contentutil.NewBuffer(),
		CacheAccessor: cm,
	})
	require.NoError(t, err)
	return src, cm
}

func newTestCacheManager(t *testing.T) (cache.Manager, error) {
	t.Helper()

	tmpdir := t.TempDir()

	snapshotter, err := native.NewSnapshotter(filepath.Join(tmpdir, "snapshots"))
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() {
		require.NoError(t, snapshotter.Close())
	})

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

	mdb := ctdmetadata.NewDB(db, store, map[string]snapshots.Snapshotter{
		"native": snapshotter,
	})

	md, err := metadata.NewStore(filepath.Join(tmpdir, "metadata.db"))
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() {
		require.NoError(t, md.Close())
	})

	lm := leaseutil.WithNamespace(ctdmetadata.NewLeaseManager(mdb), "buildkit")
	c := mdb.ContentStore()
	applier := winlayers.NewFileSystemApplierWithWindows(c, apply.NewFileSystemApplier(c))
	differ := winlayers.NewWalkingDiffWithWindows(c, walking.NewWalkingDiff(c))

	cm, err := cache.NewManager(cache.ManagerOpt{
		Snapshotter:    snapshot.FromContainerdSnapshotter("native", containerdsnapshot.NSSnapshotter("buildkit", mdb.Snapshotter("native")), nil),
		MetadataStore:  md,
		LeaseManager:   lm,
		ContentStore:   c,
		Applier:        applier,
		Differ:         differ,
		GarbageCollect: mdb.GarbageCollect,
		Root:           tmpdir,
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
