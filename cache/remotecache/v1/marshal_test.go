package cacheimport

import (
	"context"
	"testing"
	"time"

	"github.com/containerd/containerd/v2/core/content"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/buildkit/solver"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
)

// resolvableProvider can resolve any descriptor.
type resolvableProvider struct{}

func (resolvableProvider) ReaderAt(ctx context.Context, desc ocispecs.Descriptor) (content.ReaderAt, error) {
	return nil, errors.New("not implemented")
}

func (resolvableProvider) Info(ctx context.Context, d digest.Digest) (content.Info, error) {
	return content.Info{Digest: d, Size: 1}, nil
}

// unresolvableProvider models a compression-variant remote that was attached a
// provider that does not actually hold the blobs (e.g. the local content store
// for a blob that is still lazy).
type unresolvableProvider struct{}

func (unresolvableProvider) ReaderAt(ctx context.Context, desc ocispecs.Descriptor) (content.ReaderAt, error) {
	return nil, errors.WithStack(cerrdefs.ErrNotFound)
}

func (unresolvableProvider) Info(ctx context.Context, d digest.Digest) (content.Info, error) {
	return content.Info{}, errors.WithStack(cerrdefs.ErrNotFound)
}

func testDesc(name string) ocispecs.Descriptor {
	return ocispecs.Descriptor{
		Digest:    dgst(name),
		MediaType: ocispecs.MediaTypeImageLayerGzip,
		Size:      10,
	}
}

func testResult(ts time.Time, p content.InfoReaderProvider, names ...string) solver.CacheExportResult {
	descs := make([]ocispecs.Descriptor, 0, len(names))
	for _, n := range names {
		descs = append(descs, testDesc(n))
	}
	return solver.CacheExportResult{
		CreatedAt: ts,
		Result:    &solver.Remote{Descriptors: descs, Provider: p},
	}
}

// Two remotes that differ only in their digests must not be deduplicated. The
// cache exporter adds compression variants of a chain before the main remote,
// and all of them share the same CreatedAt and descriptor count.
func TestAddResultKeepsDistinctRemotes(t *testing.T) {
	ts := time.Now()
	it := &item{dgst: dgst("rec")}

	it.addResult(testResult(ts, unresolvableProvider{}, "variant"))
	it.addResult(testResult(ts, resolvableProvider{}, "main"))
	it.addResult(testResult(ts, resolvableProvider{}, "main"))

	require.Len(t, it.results, 2)
}

// A record that has both an unusable and a usable remote must still export a
// layer result. Exporting the record without one silently truncates the cache
// manifest and causes a cache miss on the next build.
func TestMarshalFallsBackToUsableRemote(t *testing.T) {
	ts := time.Now()

	cc := NewCacheChains()
	_, _, err := cc.Add(dgst("rec"), nil, []solver.CacheExportResult{
		testResult(ts, unresolvableProvider{}, "variant"),
		testResult(ts, resolvableProvider{}, "main"),
	})
	require.NoError(t, err)

	cfg, descs, err := cc.Marshal(t.Context())
	require.NoError(t, err)
	require.Len(t, cfg.Records, 1)
	require.Len(t, cfg.Records[0].Results, 1)
	require.Len(t, cfg.Layers, 1)
	require.Equal(t, testDesc("main").Digest, cfg.Layers[cfg.Records[0].Results[0].LayerIndex].Blob)
	require.Contains(t, descs, testDesc("main").Digest)
}

// If no remote of a record can be marshalled the record is still exported, just
// without results, as before.
func TestMarshalNoUsableRemote(t *testing.T) {
	ts := time.Now()

	cc := NewCacheChains()
	_, _, err := cc.Add(dgst("rec"), nil, []solver.CacheExportResult{
		testResult(ts, unresolvableProvider{}, "variant"),
	})
	require.NoError(t, err)

	cfg, _, err := cc.Marshal(t.Context())
	require.NoError(t, err)
	require.Len(t, cfg.Records, 1)
	require.Empty(t, cfg.Records[0].Results)
	require.Empty(t, cfg.Layers)
}

// The most recent result still wins when several are usable.
func TestMarshalPrefersNewestUsableRemote(t *testing.T) {
	ts := time.Now()

	cc := NewCacheChains()
	_, _, err := cc.Add(dgst("rec"), nil, []solver.CacheExportResult{
		testResult(ts.Add(-time.Hour), resolvableProvider{}, "old"),
		testResult(ts, resolvableProvider{}, "new"),
	})
	require.NoError(t, err)

	cfg, _, err := cc.Marshal(t.Context())
	require.NoError(t, err)
	require.Len(t, cfg.Records, 1)
	require.Len(t, cfg.Records[0].Results, 1)
	require.Equal(t, testDesc("new").Digest, cfg.Layers[cfg.Records[0].Results[0].LayerIndex].Blob)
}
