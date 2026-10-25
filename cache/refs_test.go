package cache

import (
	"context"
	"io"
	"testing"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/buildkit/cache/config"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/util/compression"
	"github.com/moby/buildkit/util/leaseutil"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

func TestLinkBlobMatchingDigestUpdatedMediaType(t *testing.T) {
	t.Parallel()
	ctx := namespaces.WithNamespace(t.Context(), "buildkit-test")

	co, cleanup, err := newCacheManager(ctx, t, cmOpt{})
	require.NoError(t, err)
	t.Cleanup(cleanup)
	cm := co.manager

	ctx, done, err := leaseutil.WithLease(ctx, co.lm, leaseutil.MakeTemporary)
	require.NoError(t, err)
	defer done(ctx)

	// Create zstd compressed layer blob
	zstdCompressor, _ := compression.Zstd.Compress(ctx, compression.New(compression.Zstd))
	blobBytes, desc, err := mapToBlobWithCompression(map[string]string{"foo": "bar"}, func(w io.Writer) (io.WriteCloser, string, error) {
		wc, err := zstdCompressor(w, ocispecs.MediaTypeImageLayerZstd)
		return wc, ocispecs.MediaTypeImageLayerZstd, err
	})
	require.NoError(t, err)

	// Mislabeled as gzip in the manifest
	mislabeledDesc := desc
	mislabeledDesc.MediaType = ocispecs.MediaTypeImageLayerGzip

	cw, err := co.cs.Writer(ctx, content.WithRef("write-test-blob-"+mislabeledDesc.Digest.String()))
	require.NoError(t, err)
	_, err = cw.Write(blobBytes)
	require.NoError(t, err)
	require.NoError(t, cw.Commit(ctx, 0, cw.Digest()))

	descHandlers := DescHandlers(map[digest.Digest]*DescHandler{
		mislabeledDesc.Digest: {
			Provider: func(_ session.Group) content.Provider { return co.cs },
		},
	})

	ref, err := cm.GetByBlob(ctx, mislabeledDesc, nil, descHandlers)
	require.NoError(t, err)
	defer ref.Release(context.WithoutCancel(ctx))

	sr := ref.(*immutableRef)

	// Prior to linking converted blob, zstd is not found
	_, err = sr.getBlobWithCompression(ctx, compression.Zstd)
	require.ErrorIs(t, err, cerrdefs.ErrNotFound)

	// Link converted blob with identical digest but updated zstd media type
	convertedDesc := mislabeledDesc
	convertedDesc.MediaType = ocispecs.MediaTypeImageLayerZstd
	err = sr.linkBlob(ctx, convertedDesc)
	require.NoError(t, err)

	// Verify getBlobWithCompression now resolves to zstd descriptor
	zDesc, err := sr.getBlobWithCompression(ctx, compression.Zstd)
	require.NoError(t, err)
	require.Equal(t, convertedDesc.Digest, zDesc.Digest)
	require.Equal(t, ocispecs.MediaTypeImageLayerZstd, zDesc.MediaType)

	// Verify ref media type metadata was updated
	ociDesc, err := sr.ociDesc(ctx, nil, true)
	require.NoError(t, err)
	require.Equal(t, ocispecs.MediaTypeImageLayerZstd, ociDesc.MediaType)

	// Verify walkBlob resolves zstd descriptor even when given the old mislabeled descriptor
	var matched ocispecs.Descriptor
	err = walkBlob(ctx, co.cs, mislabeledDesc, func(d ocispecs.Descriptor) bool {
		if d.MediaType == ocispecs.MediaTypeImageLayerZstd {
			matched = d
			return false
		}
		return true
	})
	require.NoError(t, err)
	require.Equal(t, ocispecs.MediaTypeImageLayerZstd, matched.MediaType)
	require.Equal(t, convertedDesc.Digest, matched.Digest)
}

func TestEnsureCompressionMislabeledLayer(t *testing.T) {
	t.Parallel()
	ctx := namespaces.WithNamespace(t.Context(), "buildkit-test")

	co, cleanup, err := newCacheManager(ctx, t, cmOpt{})
	require.NoError(t, err)
	t.Cleanup(cleanup)
	cm := co.manager

	ctx, done, err := leaseutil.WithLease(ctx, co.lm, leaseutil.MakeTemporary)
	require.NoError(t, err)
	defer done(ctx)

	// Create zstd compressed layer blob
	zstdCompressor, _ := compression.Zstd.Compress(ctx, compression.New(compression.Zstd))
	blobBytes, desc, err := mapToBlobWithCompression(map[string]string{"app": "payload"}, func(w io.Writer) (io.WriteCloser, string, error) {
		wc, err := zstdCompressor(w, ocispecs.MediaTypeImageLayerZstd)
		return wc, ocispecs.MediaTypeImageLayerZstd, err
	})
	require.NoError(t, err)

	// Mislabeled as gzip in the manifest
	mislabeledDesc := desc
	mislabeledDesc.MediaType = ocispecs.MediaTypeImageLayerGzip

	cw, err := co.cs.Writer(ctx, content.WithRef("write-test-blob-"+mislabeledDesc.Digest.String()))
	require.NoError(t, err)
	_, err = cw.Write(blobBytes)
	require.NoError(t, err)
	require.NoError(t, cw.Commit(ctx, 0, cw.Digest()))

	descHandlers := DescHandlers(map[digest.Digest]*DescHandler{
		mislabeledDesc.Digest: {
			Provider: func(_ session.Group) content.Provider { return co.cs },
		},
	})

	ref, err := cm.GetByBlob(ctx, mislabeledDesc, nil, descHandlers)
	require.NoError(t, err)
	defer ref.Release(context.WithoutCancel(ctx))

	// GetRemotes with force-compression=true for zstd on mislabeled layer
	remotes, err := ref.GetRemotes(ctx, true, config.RefConfig{
		Compression: compression.New(compression.Zstd).SetForce(true),
	}, false, nil)
	require.NoError(t, err)
	require.Len(t, remotes, 1)
	require.Len(t, remotes[0].Descriptors, 1)
	require.Equal(t, ocispecs.MediaTypeImageLayerZstd, remotes[0].Descriptors[0].MediaType)
	require.Equal(t, desc.Digest, remotes[0].Descriptors[0].Digest)

	// Verify that a correctly labeled reference sharing the same layer digest also resolves without error
	correctlyLabeledDesc := desc
	correctlyLabeledDesc.MediaType = ocispecs.MediaTypeImageLayerZstd
	ref2, err := cm.GetByBlob(ctx, correctlyLabeledDesc, nil, descHandlers)
	require.NoError(t, err)
	defer ref2.Release(context.WithoutCancel(ctx))

	zDesc, err := ref2.(*immutableRef).getBlobWithCompression(ctx, compression.Zstd)
	require.NoError(t, err)
	require.Equal(t, ocispecs.MediaTypeImageLayerZstd, zDesc.MediaType)
}
