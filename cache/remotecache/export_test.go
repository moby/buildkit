package remotecache

import (
	"testing"

	v1 "github.com/moby/buildkit/cache/remotecache/v1"
	cacheimporttypes "github.com/moby/buildkit/cache/remotecache/v1/types"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

func TestLayerBlobsDeduplicated(t *testing.T) {
	a, b, c := digest.FromString("a"), digest.FromString("b"), digest.FromString("c")
	descs := v1.DescriptorProvider{}
	for _, d := range []digest.Digest{a, b, c} {
		descs[d] = v1.DescriptorProviderPair{Descriptor: ocispecs.Descriptor{Digest: d}}
	}
	// the layer with blob c is on top of both a and b
	config := &cacheimporttypes.CacheConfig{Layers: []cacheimporttypes.CacheLayer{
		{Blob: a, ParentIndex: -1},
		{Blob: b, ParentIndex: -1},
		{Blob: c, ParentIndex: 0},
		{Blob: c, ParentIndex: 1},
	}}

	layerDescs, err := layerBlobs(config, descs)
	require.NoError(t, err)
	var got []digest.Digest
	for _, d := range layerDescs {
		got = append(got, d.Digest)
	}
	require.Equal(t, []digest.Digest{a, b, c}, got)

	config.Layers = append(config.Layers, cacheimporttypes.CacheLayer{Blob: digest.FromString("missing"), ParentIndex: -1})
	_, err = layerBlobs(config, descs)
	require.ErrorContains(t, err, "missing blob")
}
