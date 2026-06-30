package cacheimport

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/containerd/containerd/v2/core/images"
	"github.com/moby/buildkit/solver"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

func TestSimpleMarshal(t *testing.T) {
	cc := NewCacheChains()

	now := time.Now()
	addRecords := func() {
		foo, ok, err := cc.Add(outputKey(dgst("foo"), 0), nil, nil)
		require.NoError(t, err)
		require.True(t, ok)
		bar, ok, err := cc.Add(outputKey(dgst("bar"), 1), nil, nil)
		require.NoError(t, err)
		require.True(t, ok)

		r0 := &solver.Remote{
			Descriptors: []ocispecs.Descriptor{{
				Digest: dgst("d0"),
			}, {
				Digest: dgst("d1"),
			}},
		}

		_, ok, err = cc.Add(outputKey(dgst("baz"), 0), [][]solver.CacheLink{
			{{Src: foo, Selector: ""}},
			{{Src: bar, Selector: "sel0"}},
		}, []solver.CacheExportResult{{
			CreatedAt: now,
			Result:    r0,
		}})
		require.NoError(t, err)
		require.True(t, ok)
	}

	addRecords()

	cfg, _, err := cc.Marshal(context.TODO())
	require.NoError(t, err)

	require.Equal(t, 2, len(cfg.Layers))
	require.Equal(t, 3, len(cfg.Records))

	require.Equal(t, cfg.Layers[0].Blob, dgst("d0"))
	require.Equal(t, -1, cfg.Layers[0].ParentIndex)
	require.Equal(t, cfg.Layers[1].Blob, dgst("d1"))
	require.Equal(t, 0, cfg.Layers[1].ParentIndex)

	require.Equal(t, cfg.Records[0].Digest, outputKey(dgst("baz"), 0))
	require.Equal(t, 2, len(cfg.Records[0].Inputs))
	require.Equal(t, 1, len(cfg.Records[0].Results))

	require.Equal(t, cfg.Records[1].Digest, outputKey(dgst("foo"), 0))
	require.Equal(t, 0, len(cfg.Records[1].Inputs))
	require.Equal(t, 0, len(cfg.Records[1].Results))

	require.Equal(t, cfg.Records[2].Digest, outputKey(dgst("bar"), 1))
	require.Equal(t, 0, len(cfg.Records[2].Inputs))
	require.Equal(t, 0, len(cfg.Records[2].Results))

	require.Equal(t, 1, cfg.Records[0].Results[0].LayerIndex)
	require.Equal(t, "", cfg.Records[0].Inputs[0][0].Selector)
	require.Equal(t, 1, cfg.Records[0].Inputs[0][0].LinkIndex)
	require.Equal(t, "sel0", cfg.Records[0].Inputs[1][0].Selector)
	require.Equal(t, 2, cfg.Records[0].Inputs[1][0].LinkIndex)

	// adding same info again doesn't produce anything extra
	addRecords()

	cfg2, descPairs, err := cc.Marshal(context.TODO())
	require.NoError(t, err)

	require.Equal(t, cfg, cfg2)

	// marshal roundtrip
	dt, err := json.Marshal(cfg)
	require.NoError(t, err)

	newChains := NewCacheChains()
	err = Parse(dt, descPairs, newChains)
	require.NoError(t, err)

	cfg3, _, err := cc.Marshal(context.TODO())
	require.NoError(t, err)
	require.Equal(t, cfg, cfg3)

	// add extra item
	_, ok, err := cc.Add(outputKey(dgst("bay"), 0), nil, nil)
	require.NoError(t, err)
	require.True(t, ok)
	cfg, _, err = cc.Marshal(context.TODO())
	require.NoError(t, err)

	require.Equal(t, 2, len(cfg.Layers))
	require.Equal(t, 4, len(cfg.Records))
}

func TestMarshalLayerMediaTypes(t *testing.T) {
	for _, tc := range []struct {
		name          string
		ociMediaTypes *bool
		descs         []ocispecs.Descriptor
		expected      map[digest.Digest]string
	}{
		{
			name: "passthrough",
			descs: []ocispecs.Descriptor{
				{Digest: dgst("d0"), MediaType: images.MediaTypeDockerSchema2Layer},
				{Digest: dgst("d1"), MediaType: images.MediaTypeDockerSchema2LayerGzip},
				{Digest: dgst("d2"), MediaType: images.MediaTypeDockerSchema2LayerZstd},
			},
			expected: map[digest.Digest]string{
				dgst("d0"): images.MediaTypeDockerSchema2Layer,
				dgst("d1"): images.MediaTypeDockerSchema2LayerGzip,
				dgst("d2"): images.MediaTypeDockerSchema2LayerZstd,
			},
		},
		{
			name:          "oci",
			ociMediaTypes: boolPtr(true),
			descs: []ocispecs.Descriptor{
				{Digest: dgst("d0"), MediaType: images.MediaTypeDockerSchema2Layer},
				{Digest: dgst("d1"), MediaType: images.MediaTypeDockerSchema2LayerGzip},
				{Digest: dgst("d2"), MediaType: images.MediaTypeDockerSchema2LayerZstd},
			},
			expected: map[digest.Digest]string{
				dgst("d0"): ocispecs.MediaTypeImageLayer,
				dgst("d1"): ocispecs.MediaTypeImageLayerGzip,
				dgst("d2"): ocispecs.MediaTypeImageLayerZstd,
			},
		},
		{
			name:          "docker",
			ociMediaTypes: boolPtr(false),
			descs: []ocispecs.Descriptor{
				{Digest: dgst("d0"), MediaType: ocispecs.MediaTypeImageLayer},
				{Digest: dgst("d1"), MediaType: ocispecs.MediaTypeImageLayerGzip},
				{Digest: dgst("d2"), MediaType: ocispecs.MediaTypeImageLayerZstd},
			},
			expected: map[digest.Digest]string{
				dgst("d0"): images.MediaTypeDockerSchema2Layer,
				dgst("d1"): images.MediaTypeDockerSchema2LayerGzip,
				dgst("d2"): images.MediaTypeDockerSchema2LayerZstd,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cc := NewCacheChains()
			_, ok, err := cc.Add(outputKey(dgst("foo"), 0), nil, []solver.CacheExportResult{{
				CreatedAt: time.Now(),
				Result: &solver.Remote{
					Descriptors: tc.descs,
				},
			}})
			require.NoError(t, err)
			require.True(t, ok)

			_, descPairs, err := cc.Marshal(context.TODO(), MarshalOpt{OCIMediaTypes: tc.ociMediaTypes})
			require.NoError(t, err)

			for dgst, mediaType := range tc.expected {
				require.Equal(t, mediaType, descPairs[dgst].Descriptor.MediaType)
			}
		})
	}
}

func boolPtr(v bool) *bool {
	return &v
}

func dgst(s string) digest.Digest {
	return digest.FromBytes([]byte(s))
}
