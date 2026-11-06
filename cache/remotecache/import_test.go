package remotecache

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/containerd/containerd/v2/core/content"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

// memProvider serves one blob from memory.
type memProvider struct {
	dt []byte
}

func (p *memProvider) ReaderAt(_ context.Context, _ ocispecs.Descriptor) (content.ReaderAt, error) {
	return &memReaderAt{Reader: bytes.NewReader(p.dt), size: int64(len(p.dt))}, nil
}

type memReaderAt struct {
	*bytes.Reader
	size int64
}

func (r *memReaderAt) Size() int64  { return r.size }
func (r *memReaderAt) Close() error { return nil }

func descFor(dt []byte, mediaType string) ocispecs.Descriptor {
	return ocispecs.Descriptor{
		MediaType: mediaType,
		Digest:    digest.FromBytes(dt),
		Size:      int64(len(dt)),
	}
}

func TestReadBlobLimits(t *testing.T) {
	ctx := context.Background()

	// The limits must both sit above the 1 MiB ceiling that refused real
	// cache configs (mode=max exports of large builds, buildkit issues #3719
	// and #4916); the config ceiling is the larger of the two.
	require.Greater(t, maxManifestBlobSize, int64(1<<20))
	require.Greater(t, maxCacheConfigBlobSize, maxManifestBlobSize)

	t.Run("cache config just over 1 MiB imports", func(t *testing.T) {
		dt := bytes.Repeat([]byte{'x'}, 1<<20+1)
		desc := descFor(dt, "application/vnd.buildkit.cacheconfig.v0")
		got, err := readBlob(ctx, &memProvider{dt: dt}, desc, maxCacheConfigBlobSize)
		require.NoError(t, err)
		require.Equal(t, dt, got)
	})

	t.Run("cache config over its ceiling is refused before reading", func(t *testing.T) {
		desc := ocispecs.Descriptor{
			MediaType: "application/vnd.buildkit.cacheconfig.v0",
			Digest:    digest.FromString("unread"),
			Size:      maxCacheConfigBlobSize + 1,
		}
		_, err := readBlob(ctx, &memProvider{dt: nil}, desc, maxCacheConfigBlobSize)
		require.Error(t, err)
		require.True(t, strings.Contains(err.Error(), "is too large"), err.Error())
	})

	t.Run("manifest over its ceiling is refused before reading", func(t *testing.T) {
		desc := ocispecs.Descriptor{
			MediaType: ocispecs.MediaTypeImageManifest,
			Digest:    digest.FromString("unread"),
			Size:      maxManifestBlobSize + 1,
		}
		_, err := readBlob(ctx, &memProvider{dt: nil}, desc, maxManifestBlobSize)
		require.Error(t, err)
		require.True(t, strings.Contains(err.Error(), "is too large"), err.Error())
	})

	t.Run("manifest within its ceiling reads", func(t *testing.T) {
		dt := []byte(`{"schemaVersion":2}`)
		desc := descFor(dt, ocispecs.MediaTypeImageManifest)
		got, err := readBlob(ctx, &memProvider{dt: dt}, desc, maxManifestBlobSize)
		require.NoError(t, err)
		require.Equal(t, dt, got)
	})
}
