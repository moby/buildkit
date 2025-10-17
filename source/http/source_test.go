package http

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/containerd/containerd/v2/core/diff/apply"
	ctdmetadata "github.com/containerd/containerd/v2/core/metadata"
	"github.com/containerd/containerd/v2/core/snapshots"
	"github.com/containerd/containerd/v2/plugins/content/local"
	"github.com/containerd/containerd/v2/plugins/diff/walking"
	"github.com/containerd/containerd/v2/plugins/snapshots/native"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/cache/metadata"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/snapshot"
	containerdsnapshot "github.com/moby/buildkit/snapshot/containerd"
	"github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/source"
	"github.com/moby/buildkit/util/leaseutil"
	"github.com/moby/buildkit/util/testutil/httpserver"
	"github.com/moby/buildkit/util/winlayers"
	digest "github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
	bolt "go.etcd.io/bbolt"
)

func TestHTTPSource(t *testing.T) {
	t.Parallel()
	ctx := context.TODO()

	hs, err := newHTTPSource(t)
	require.NoError(t, err)

	resp := &httpserver.Response{
		Etag:    identity.NewID(),
		Content: []byte("content1"),
	}
	server := httpserver.NewTestServer(map[string]*httpserver.Response{
		"/foo": resp,
	})
	defer server.Close()

	id := &HTTPIdentifier{URL: server.URL + "/foo"}

	h, err := hs.Resolve(ctx, id, nil, nil)
	require.NoError(t, err)

	k, p, _, _, err := h.CacheKey(ctx, nil, 0)
	require.NoError(t, err)

	expectedPin1 := digest.FromBytes(resp.Content).String()
	plaintext1 := `{"Filename":"foo","Perm":0,"UID":0,"GID":0,"Checksum":"` + expectedPin1 + `"}`
	expectedContent1 := digest.FromBytes([]byte(plaintext1)).String()

	require.Equal(t, expectedContent1, k)
	require.Equal(t, expectedPin1, p)
	require.Equal(t, 1, server.Stats("/foo").AllRequests)
	require.Equal(t, 0, server.Stats("/foo").CachedRequests)

	ref, err := h.Snapshot(ctx, nil)
	require.NoError(t, err)
	defer func() {
		if ref != nil {
			ref.Release(context.WithoutCancel(ctx))
			ref = nil
		}
	}()

	dt, err := readFile(ctx, ref, "foo")
	require.NoError(t, err)
	require.Equal(t, dt, []byte("content1"))

	ref.Release(context.TODO())
	ref = nil

	// repeat, should use the etag
	h, err = hs.Resolve(ctx, id, nil, nil)
	require.NoError(t, err)

	k, p, _, _, err = h.CacheKey(ctx, nil, 0)
	require.NoError(t, err)

	require.Equal(t, expectedContent1, k)
	require.Equal(t, expectedPin1, p)
	require.Equal(t, 2, server.Stats("/foo").AllRequests)
	require.Equal(t, 1, server.Stats("/foo").CachedRequests)

	ref, err = h.Snapshot(ctx, nil)
	require.NoError(t, err)
	defer func() {
		if ref != nil {
			ref.Release(context.WithoutCancel(ctx))
			ref = nil
		}
	}()

	dt, err = readFile(ctx, ref, "foo")
	require.NoError(t, err)
	require.Equal(t, dt, []byte("content1"))

	ref.Release(context.TODO())
	ref = nil

	resp2 := &httpserver.Response{
		Etag:    identity.NewID(),
		Content: []byte("content2"),
	}

	expectedPin2 := digest.FromBytes(resp2.Content).String()
	plaintext2 := `{"Filename":"foo","Perm":0,"UID":0,"GID":0,"Checksum":"` + expectedPin2 + `"}`
	expectedContent2 := digest.FromBytes([]byte(plaintext2)).String()

	// update etag, downloads again
	server.SetRoute("/foo", resp2)

	h, err = hs.Resolve(ctx, id, nil, nil)
	require.NoError(t, err)

	k, p, _, _, err = h.CacheKey(ctx, nil, 0)
	require.NoError(t, err)

	require.Equal(t, expectedContent2, k)
	require.Equal(t, expectedPin2, p)
	require.Equal(t, 4, server.Stats("/foo").AllRequests)
	require.Equal(t, 1, server.Stats("/foo").CachedRequests)

	ref, err = h.Snapshot(ctx, nil)
	require.NoError(t, err)
	defer func() {
		if ref != nil {
			ref.Release(context.WithoutCancel(ctx))
			ref = nil
		}
	}()

	dt, err = readFile(ctx, ref, "foo")
	require.NoError(t, err)
	require.Equal(t, dt, []byte("content2"))

	ref.Release(context.TODO())
	ref = nil
}

func TestHTTPDefaultName(t *testing.T) {
	t.Parallel()
	ctx := context.TODO()

	hs, err := newHTTPSource(t)
	require.NoError(t, err)

	resp := &httpserver.Response{
		Etag:    identity.NewID(),
		Content: []byte("content1"),
	}
	server := httpserver.NewTestServer(map[string]*httpserver.Response{
		"/": resp,
	})
	defer server.Close()

	id := &HTTPIdentifier{URL: server.URL}

	h, err := hs.Resolve(ctx, id, nil, nil)
	require.NoError(t, err)

	k, p, _, _, err := h.CacheKey(ctx, nil, 0)
	require.NoError(t, err)

	expectedPin := digest.FromBytes(resp.Content).String()
	plaintext := `{"Filename":"download","Perm":0,"UID":0,"GID":0,"Checksum":"` + expectedPin + `"}`
	expectedContent := digest.FromBytes([]byte(plaintext)).String()

	require.Equal(t, expectedContent, k)
	require.Equal(t, expectedPin, p)
	require.Equal(t, 1, server.Stats("/").AllRequests)
	require.Equal(t, 0, server.Stats("/").CachedRequests)

	ref, err := h.Snapshot(ctx, nil)
	require.NoError(t, err)
	defer func() {
		if ref != nil {
			ref.Release(context.WithoutCancel(ctx))
			ref = nil
		}
	}()

	dt, err := readFile(ctx, ref, "download")
	require.NoError(t, err)
	require.Equal(t, dt, []byte("content1"))

	ref.Release(context.TODO())
	ref = nil
}

func TestHTTPInvalidURL(t *testing.T) {
	t.Parallel()
	ctx := context.TODO()

	hs, err := newHTTPSource(t)
	require.NoError(t, err)

	server := httpserver.NewTestServer(map[string]*httpserver.Response{})
	defer server.Close()

	id := &HTTPIdentifier{URL: server.URL + "/foo"}

	h, err := hs.Resolve(ctx, id, nil, nil)
	require.NoError(t, err)

	_, _, _, _, err = h.CacheKey(ctx, nil, 0)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid response")
}

func TestHTTPChecksum(t *testing.T) {
	t.Parallel()
	ctx := context.TODO()

	hs, err := newHTTPSource(t)
	require.NoError(t, err)

	resp := &httpserver.Response{
		Etag:    identity.NewID(),
		Content: []byte("content-correct"),
	}
	server := httpserver.NewTestServer(map[string]*httpserver.Response{
		"/foo": resp,
	})
	defer server.Close()

	expectedPinDifferent := digest.FromBytes([]byte("content-different"))
	id := &HTTPIdentifier{URL: server.URL + "/foo", Checksum: expectedPinDifferent}

	h, err := hs.Resolve(ctx, id, nil, nil)
	require.NoError(t, err)

	k, p, _, _, err := h.CacheKey(ctx, nil, 0)
	require.NoError(t, err)

	plaintextDifferent := `{"Filename":"foo","Perm":0,"UID":0,"GID":0,"Checksum":"` + expectedPinDifferent + `"}`
	expectedContentDifferent := digest.FromBytes([]byte(plaintextDifferent)).String()

	expectedPinCorrect := digest.FromBytes(resp.Content).String()
	plaintextCorrect := `{"Filename":"foo","Perm":0,"UID":0,"GID":0,"Checksum":"` + expectedPinCorrect + `"}`
	expectedContentCorrect := digest.FromBytes([]byte(plaintextCorrect)).String()

	require.Equal(t, expectedContentDifferent, k)
	require.Equal(t, expectedPinDifferent.String(), p)
	require.Equal(t, 0, server.Stats("/foo").AllRequests)
	require.Equal(t, 0, server.Stats("/foo").CachedRequests)

	_, err = h.Snapshot(ctx, nil)
	require.Error(t, err)

	require.Equal(t, expectedContentDifferent, k)
	require.Equal(t, expectedPinDifferent.String(), p)
	require.Equal(t, 1, server.Stats("/foo").AllRequests)
	require.Equal(t, 0, server.Stats("/foo").CachedRequests)

	id = &HTTPIdentifier{URL: server.URL + "/foo", Checksum: digest.FromBytes([]byte("content-correct"))}

	h, err = hs.Resolve(ctx, id, nil, nil)
	require.NoError(t, err)

	k, p, _, _, err = h.CacheKey(ctx, nil, 0)
	require.NoError(t, err)

	require.Equal(t, expectedContentCorrect, k)
	require.Equal(t, expectedPinCorrect, p)
	require.Equal(t, 1, server.Stats("/foo").AllRequests)
	require.Equal(t, 0, server.Stats("/foo").CachedRequests)

	ref, err := h.Snapshot(ctx, nil)
	require.NoError(t, err)
	defer func() {
		if ref != nil {
			ref.Release(context.WithoutCancel(ctx))
			ref = nil
		}
	}()

	dt, err := readFile(ctx, ref, "foo")
	require.NoError(t, err)
	require.Equal(t, dt, []byte("content-correct"))

	require.Equal(t, expectedContentCorrect, k)
	require.Equal(t, expectedPinCorrect, p)
	require.Equal(t, 2, server.Stats("/foo").AllRequests)
	require.Equal(t, 0, server.Stats("/foo").CachedRequests)

	ref.Release(context.TODO())
	ref = nil
}

func TestPruneAfterCacheKey(t *testing.T) {
	t.Parallel()
	ctx := context.TODO()

	cm, err := newCacheManager(t)
	require.NoError(t, err)

	hs, err := NewSource(Opt{
		CacheAccessor: cm,
	})
	require.NoError(t, err)

	resp := &httpserver.Response{
		Etag:    identity.NewID(),
		Content: []byte("content-correct"),
	}
	server := httpserver.NewTestServer(map[string]*httpserver.Response{
		"/foo": resp,
	})
	defer server.Close()

	id := &HTTPIdentifier{URL: server.URL + "/foo"}

	h, err := hs.Resolve(ctx, id, nil, nil)
	require.NoError(t, err)

	jc := &simpleJobContext{}

	k, p, _, _, err := h.CacheKey(ctx, jc, 0)
	require.NoError(t, err)

	expectedPin := digest.FromBytes(resp.Content).String()
	plaintextCacheKey := `{"Filename":"foo","Perm":0,"UID":0,"GID":0,"Checksum":"` + expectedPin + `"}`
	expectedCacheKey := digest.FromBytes([]byte(plaintextCacheKey)).String()

	require.Equal(t, expectedCacheKey, k)
	require.Equal(t, expectedPin, p)
	require.Equal(t, 1, server.Stats("/foo").AllRequests)
	require.Equal(t, 0, server.Stats("/foo").CachedRequests)

	err = cm.Prune(ctx, nil, client.PruneInfo{
		All: true,
	})
	require.NoError(t, err)

	du, err := cm.DiskUsage(ctx, client.DiskUsageInfo{})
	require.NoError(t, err)
	require.Len(t, du, 1)
	for _, u := range du {
		require.Equal(t, "http url "+id.URL, u.Description)
	}

	resp.Etag = identity.NewID()
	resp.Content = []byte("content-updated")

	ref, err := h.Snapshot(ctx, jc)
	require.NoError(t, err)
	ref.Release(context.TODO())

	dt, err := readFile(ctx, ref, "foo")
	require.NoError(t, err)
	require.Equal(t, dt, []byte("content-correct"))

	require.Equal(t, 1, server.Stats("/foo").AllRequests)
	require.Equal(t, 0, server.Stats("/foo").CachedRequests)

	du, err = cm.DiskUsage(ctx, client.DiskUsageInfo{})
	require.NoError(t, err)
	require.Len(t, du, 1)

	err = cm.Prune(ctx, nil, client.PruneInfo{
		All: true,
	})
	require.NoError(t, err)

	du, err = cm.DiskUsage(ctx, client.DiskUsageInfo{})
	require.NoError(t, err)
	require.Len(t, du, 1)

	err = jc.Release()
	require.NoError(t, err)

	err = cm.Prune(ctx, nil, client.PruneInfo{
		All: true,
	})
	require.NoError(t, err)

	du, err = cm.DiskUsage(ctx, client.DiskUsageInfo{})
	require.NoError(t, err)
	for _, u := range du {
		t.Logf("du: %+v", *u)
	}
	require.Len(t, du, 0)
}

func readFile(ctx context.Context, ref cache.ImmutableRef, fp string) ([]byte, error) {
	mount, err := ref.Mount(ctx, true, nil)
	if err != nil {
		return nil, err
	}

	lm := snapshot.LocalMounter(mount)
	dir, err := lm.Mount()
	if err != nil {
		return nil, err
	}

	defer lm.Unmount()

	dt, err := os.ReadFile(filepath.Join(dir, fp))
	if err != nil {
		return nil, err
	}

	return dt, nil
}

func newHTTPSource(t *testing.T) (source.Source, error) {
	cm, err := newCacheManager(t)
	if err != nil {
		return nil, err
	}
	return NewSource(Opt{
		CacheAccessor: cm,
	})
}

func newCacheManager(t *testing.T) (cache.Manager, error) {
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

type simpleJobContext struct {
	releasers []func() error
}

func (s *simpleJobContext) Session() session.Group {
	return nil
}
func (s *simpleJobContext) Cleanup(f func() error) error {
	s.releasers = append(s.releasers, f)
	return nil
}

func (s *simpleJobContext) Release() error {
	var firstErr error
	for _, r := range s.releasers {
		if err := r(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	s.releasers = nil
	return firstErr
}

func (s *simpleJobContext) ResolverCache() solver.ResolverCache {
	return nil
}
