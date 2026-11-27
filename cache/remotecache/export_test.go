package remotecache

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/plugins/content/local"
	"github.com/moby/buildkit/solver"

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

// TestExportManifestIndependentOfPullSource checks that the cache manifest
// lists each blob once and does not depend on where the builder pulled the
// layers from (distribution source annotations).
func TestExportManifestIndependentOfPullSource(t *testing.T) {
	ctx := t.Context()

	src, err := local.NewStore(t.TempDir())
	require.NoError(t, err)
	blob := func(s string) ocispecs.Descriptor {
		dt := []byte(s)
		desc := ocispecs.Descriptor{
			MediaType: ocispecs.MediaTypeImageLayerGzip,
			Digest:    digest.FromBytes(dt),
			Size:      int64(len(dt)),
		}
		require.NoError(t, content.WriteBlob(ctx, src, s, bytesReader(dt), desc))
		return desc
	}
	base, l1, l2 := blob("base"), blob("layer1"), blob("layer2")

	export := func(dsl string) ocispecs.Descriptor {
		annotate := func(d ocispecs.Descriptor) ocispecs.Descriptor {
			d.Annotations = map[string]string{
				"containerd.io/distribution.source.docker.io": dsl,
				"containerd.io/uncompressed":                  d.Digest.String(),
			}
			return d
		}
		dst, err := local.NewStore(t.TempDir())
		require.NoError(t, err)
		e := NewExporter(dst, "", true, false)
		now := time.Unix(1700000000, 0).UTC()
		// base is shared by two chains: base->l1 and base->l2, and l1 is
		// also reused on top of l2 (same blob, different parent chain).
		for i, r := range [][]ocispecs.Descriptor{
			{annotate(base), annotate(l1)},
			{annotate(base), annotate(l2)},
			{annotate(base), annotate(l2), annotate(l1)},
		} {
			_, _, err := e.Add(digest.FromString(string(rune('a'+i))), nil, []solver.CacheExportResult{{
				CreatedAt: now,
				Result:    &solver.Remote{Descriptors: r, Provider: src},
			}})
			require.NoError(t, err)
		}
		res, err := e.Finalize(ctx)
		require.NoError(t, err)
		var desc ocispecs.Descriptor
		require.NoError(t, json.Unmarshal([]byte(res[ExporterResponseManifestDesc]), &desc))

		dt, err := content.ReadBlob(ctx, dst, desc)
		require.NoError(t, err)
		var idx ocispecs.Index
		require.NoError(t, json.Unmarshal(dt, &idx))
		seen := map[digest.Digest]int{}
		for _, m := range idx.Manifests {
			seen[m.Digest]++
			for k := range m.Annotations {
				require.NotContains(t, k, "distribution.source", "manifest must not carry pull-source hints")
			}
		}
		for d, n := range seen {
			require.Equal(t, 1, n, "blob %s listed %d times", d, n)
		}
		require.Len(t, seen, 4) // 3 layers + config
		return desc
	}

	d1 := export("library/alpine")
	d2 := export("library/alpine,mirror/alpine")
	require.Equal(t, d1.Digest, d2.Digest)
}

func bytesReader(dt []byte) *bytes.Reader { return bytes.NewReader(dt) }
