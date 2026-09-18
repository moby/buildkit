// Package lazyexport holds an end-to-end check of the cache exporter over a ref
// that is still lazy. It lives in its own package because
// cache/remotecache/v1 -> worker -> cache is an import cycle, so this cannot be
// a test of package cache.
package lazyexport

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/diff/apply"
	"github.com/containerd/containerd/v2/core/leases"
	ctdmetadata "github.com/containerd/containerd/v2/core/metadata"
	"github.com/containerd/containerd/v2/core/snapshots"
	"github.com/containerd/containerd/v2/pkg/labels"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/containerd/v2/plugins/content/local"
	"github.com/containerd/containerd/v2/plugins/diff/walking"
	"github.com/containerd/containerd/v2/plugins/snapshots/native"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/cache/config"
	"github.com/moby/buildkit/cache/metadata"
	cacheimport "github.com/moby/buildkit/cache/remotecache/v1"
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
	"github.com/stretchr/testify/require"
	bolt "go.etcd.io/bbolt"
)

// TestExportCacheOfLazyRefKeepsResult drives production code end to end: a real
// lazy ref, its real GetRemotes, and the real cache exporter.
//
// A lazy ref holds its blob in a session provider, not in the content store.
// GetRemotes with all=true still reports a compression variant for it: the
// topmost blob already carries the requested compression, so
// getBlobWithCompression returns that very descriptor, and GetRemotes attaches
// the plain content store as the variant's provider - which cannot resolve a
// blob that is still lazy. solver/exporter.go then appends the variants of a
// record before its main remote, so the unusable one comes first.
//
// The record must still be exported with a layer result. Exporting it empty
// breaks the key chain in the manifest, and the next build misses the cache.
func TestExportCacheOfLazyRefKeepsResult(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skipf("unsupported GOOS: %s", runtime.GOOS)
	}

	ctx := namespaces.WithNamespace(t.Context(), "buildkit-test")
	cm, lm, cleanup := newManager(ctx, t)
	t.Cleanup(cleanup)

	ctx, done, err := leaseutil.WithLease(ctx, lm, leaseutil.MakeTemporary)
	require.NoError(t, err)
	defer done(t.Context())

	// The blob is only in this buffer, reachable through the desc handler. The
	// manager's content store never sees it: that is what makes the ref lazy.
	contentBuffer := contentutil.NewBuffer()
	blobBytes, desc, err := gzipTarBlob(map[string]string{"foo": "bar"})
	require.NoError(t, err)

	cw, err := contentBuffer.Writer(ctx)
	require.NoError(t, err)
	_, err = cw.Write(blobBytes)
	require.NoError(t, err)
	require.NoError(t, cw.Commit(ctx, 0, cw.Digest()))

	descHandlers := cache.DescHandlers(map[digest.Digest]*cache.DescHandler{
		desc.Digest: {
			Provider: func(_ session.Group) content.Provider { return contentBuffer },
		},
	})

	lazyRef, err := cm.GetByBlob(ctx, desc, nil, descHandlers)
	require.NoError(t, err)
	t.Cleanup(func() { lazyRef.Release(t.Context()) })

	refCfg := config.RefConfig{Compression: compression.New(compression.Gzip)}
	remotes, err := lazyRef.GetRemotes(ctx, false, refCfg, true, nil)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(remotes), 2,
		"expected the main remote plus at least one compression variant")

	for i, r := range remotes {
		top := r.Descriptors[len(r.Descriptors)-1]
		_, infoErr := r.Provider.Info(ctx, top.Digest)
		t.Logf("remote %d: provider=%T topmost=%s resolves=%v", i, r.Provider, top.Digest, infoErr == nil)
	}

	// Mirror solver/exporter.go: variants first, main remote last, one CreatedAt.
	ts := time.Now()
	results := make([]solver.CacheExportResult, 0, len(remotes))
	for _, r := range remotes[1:] {
		results = append(results, solver.CacheExportResult{CreatedAt: ts, Result: r})
	}
	results = append(results, solver.CacheExportResult{CreatedAt: ts, Result: remotes[0]})

	cc := cacheimport.NewCacheChains()
	_, _, err = cc.Add(digest.FromString("record"), nil, results)
	require.NoError(t, err)

	cfg, descProvider, err := cc.Marshal(ctx)
	require.NoError(t, err)
	require.Len(t, cfg.Records, 1)
	require.Len(t, cfg.Records[0].Results, 1,
		"record exported with no result: the cache manifest is truncated and the next build misses")
	require.Len(t, cfg.Layers, 1)
	require.Equal(t, desc.Digest, cfg.Layers[cfg.Records[0].Results[0].LayerIndex].Blob)
	require.Contains(t, descProvider, desc.Digest)
}

func newManager(ctx context.Context, t *testing.T) (cache.Manager, leases.Manager, func()) {
	ns, ok := namespaces.Namespace(ctx)
	require.True(t, ok)

	tmpdir := t.TempDir()

	snapshotter, err := native.NewSnapshotter(filepath.Join(tmpdir, "snapshots"))
	require.NoError(t, err)

	store, err := local.NewStore(tmpdir)
	require.NoError(t, err)

	db, err := bolt.Open(filepath.Join(tmpdir, "containerdmeta.db"), 0644, nil)
	require.NoError(t, err)

	mdb := ctdmetadata.NewDB(db, store, map[string]snapshots.Snapshotter{"native": snapshotter})
	require.NoError(t, mdb.Init(t.Context()))

	cs := containerdsnapshot.NewContentStore(mdb.ContentStore(), ns)
	lm := leaseutil.WithNamespace(ctdmetadata.NewLeaseManager(mdb), ns)

	md, err := metadata.NewStore(filepath.Join(tmpdir, "metadata.db"))
	require.NoError(t, err)

	cm, err := cache.NewManager(cache.ManagerOpt{
		Snapshotter: snapshot.FromContainerdSnapshotter("native",
			containerdsnapshot.NSSnapshotter(ns, mdb.Snapshotter("native")), nil),
		MetadataStore:  md,
		ContentStore:   cs,
		LeaseManager:   lm,
		GarbageCollect: mdb.GarbageCollect,
		Applier:        winlayers.NewFileSystemApplierWithWindows(cs, apply.NewFileSystemApplier(cs)),
		Differ:         winlayers.NewWalkingDiffWithWindows(cs, walking.NewWalkingDiff(cs)),
		Root:           tmpdir,
		MountPoolRoot:  filepath.Join(tmpdir, "cachemounts"),
	})
	require.NoError(t, err)

	return cm, lm, func() {
		cm.Close()
		md.Close()
		db.Close()
	}
}

func gzipTarBlob(m map[string]string) ([]byte, ocispecs.Descriptor, error) {
	buf := bytes.NewBuffer(nil)
	sha := digest.SHA256.Digester()
	gz := gzip.NewWriter(buf)
	tw := tar.NewWriter(io.MultiWriter(sha.Hash(), gz))

	for k, v := range m {
		if err := tw.WriteHeader(&tar.Header{Name: k, Size: int64(len(v))}); err != nil {
			return nil, ocispecs.Descriptor{}, err
		}
		if _, err := tw.Write([]byte(v)); err != nil {
			return nil, ocispecs.Descriptor{}, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, ocispecs.Descriptor{}, err
	}
	if err := gz.Close(); err != nil {
		return nil, ocispecs.Descriptor{}, err
	}

	return buf.Bytes(), ocispecs.Descriptor{
		Digest:      digest.FromBytes(buf.Bytes()),
		MediaType:   ocispecs.MediaTypeImageLayerGzip,
		Size:        int64(buf.Len()),
		Annotations: map[string]string{labels.LabelUncompressed: sha.Digest().String()},
	}, nil
}
