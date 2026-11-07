package cacheimport

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	cerrdefs "github.com/containerd/errdefs"
	cacheimporttypes "github.com/moby/buildkit/cache/remotecache/v1/types"
	"github.com/moby/buildkit/solver"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
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

	cfg, _, err := cc.Marshal(t.Context())
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

	cfg2, descPairs, err := cc.Marshal(t.Context())
	require.NoError(t, err)

	require.Equal(t, cfg, cfg2)

	// marshal roundtrip
	dt, err := json.Marshal(cfg)
	require.NoError(t, err)

	newChains := NewCacheChains()
	err = Parse(dt, descPairs, newChains)
	require.NoError(t, err)

	cfg3, _, err := cc.Marshal(t.Context())
	require.NoError(t, err)
	require.Equal(t, cfg, cfg3)

	// add extra item
	_, ok, err := cc.Add(outputKey(dgst("bay"), 0), nil, nil)
	require.NoError(t, err)
	require.True(t, ok)
	cfg, _, err = cc.Marshal(t.Context())
	require.NoError(t, err)

	require.Equal(t, 2, len(cfg.Layers))
	require.Equal(t, 4, len(cfg.Records))
}

func TestMarshalLayerMediaTypes(t *testing.T) {
	for _, tc := range []struct {
		name     string
		descs    []ocispecs.Descriptor
		expected map[digest.Digest]string
	}{
		{
			name: "docker",
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
			name: "oci",
			descs: []ocispecs.Descriptor{
				{Digest: dgst("d0"), MediaType: ocispecs.MediaTypeImageLayer},
				{Digest: dgst("d1"), MediaType: ocispecs.MediaTypeImageLayerGzip},
				{Digest: dgst("d2"), MediaType: ocispecs.MediaTypeImageLayerZstd},
			},
			expected: map[digest.Digest]string{
				dgst("d0"): ocispecs.MediaTypeImageLayer,
				dgst("d1"): ocispecs.MediaTypeImageLayerGzip,
				dgst("d2"): ocispecs.MediaTypeImageLayerZstd,
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

			_, descPairs, err := cc.Marshal(t.Context())
			require.NoError(t, err)

			for dgst, mediaType := range tc.expected {
				require.Equal(t, mediaType, descPairs[dgst].Descriptor.MediaType)
			}
		})
	}
}

func dgst(s string) digest.Digest {
	return digest.FromBytes([]byte(s))
}

// countingProvider fails Info for a blob once it has been checked more than failAfter times, modelling a blob
// that becomes unavailable while the config is being marshaled.
type countingProvider struct {
	failAfter map[digest.Digest]int
	calls     map[digest.Digest]int
}

func (p *countingProvider) Info(_ context.Context, dgst digest.Digest) (content.Info, error) {
	p.calls[dgst]++
	if n, ok := p.failAfter[dgst]; ok && p.calls[dgst] > n {
		return content.Info{}, errors.Wrapf(cerrdefs.ErrNotFound, "blob %s", dgst)
	}
	return content.Info{Digest: dgst}, nil
}

func (p *countingProvider) ReaderAt(context.Context, ocispecs.Descriptor) (content.ReaderAt, error) {
	return nil, cerrdefs.ErrNotImplemented
}

func TestMarshalUnavailableBlob(t *testing.T) {
	chain := []ocispecs.Descriptor{{Digest: dgst("d0")}, {Digest: dgst("d1")}, {Digest: dgst("d2")}}

	marshal := func(t *testing.T, p *countingProvider) *cacheimporttypes.CacheConfig {
		cc := NewCacheChains()
		base, ok, err := cc.Add(outputKey(dgst("base"), 0), nil, nil)
		require.NoError(t, err)
		require.True(t, ok)
		// built with append: a one-element literal makes gosec report G602 on deps[i][j] in Add
		var deps [][]solver.CacheLink
		deps = append(deps, []solver.CacheLink{{Src: base}})
		_, ok, err = cc.Add(outputKey(dgst("run"), 0), deps, []solver.CacheExportResult{{
			CreatedAt: time.Now(),
			Result:    &solver.Remote{Descriptors: chain, Provider: p},
		}})
		require.NoError(t, err)
		require.True(t, ok)
		cfg, _, err := cc.Marshal(t.Context())
		require.NoError(t, err)
		return cfg
	}

	t.Run("unavailable", func(t *testing.T) {
		// the bottom blob is gone: the result is dropped, nothing of its chain is exported
		cfg := marshal(t, &countingProvider{failAfter: map[digest.Digest]int{dgst("d0"): 0}, calls: map[digest.Digest]int{}})
		require.Empty(t, cfg.Layers)
		for _, r := range cfg.Records {
			require.Empty(t, r.Results)
		}
	})

	t.Run("becomes unavailable", func(t *testing.T) {
		// the bottom blob passes the first check and fails afterwards: the chain is exported whole or not at all,
		// never rooted on d1 or d2
		cfg := marshal(t, &countingProvider{failAfter: map[digest.Digest]int{dgst("d0"): 1}, calls: map[digest.Digest]int{}})
		for _, l := range cfg.Layers {
			for l.ParentIndex != -1 {
				l = cfg.Layers[l.ParentIndex]
			}
			require.Equal(t, dgst("d0"), l.Blob, "layer chain rooted on %s instead of the image root", l.Blob)
		}
		for _, r := range cfg.Records {
			for _, res := range r.Results {
				n, l := 1, cfg.Layers[res.LayerIndex]
				for l.ParentIndex != -1 {
					l, n = cfg.Layers[l.ParentIndex], n+1
				}
				require.Equal(t, len(chain), n)
			}
		}
	})
}

func TestMarshalSourceDateEpoch(t *testing.T) {
	sde := time.Unix(42, 0).UTC()
	before := time.Unix(10, 0).UTC()

	build := func(createdAt time.Time) (*CacheChains, *solver.Remote) {
		cc := NewCacheChains()
		createdAtText, err := createdAt.MarshalText()
		require.NoError(t, err)
		r := &solver.Remote{
			Descriptors: []ocispecs.Descriptor{{
				Digest:      dgst("d0"),
				Annotations: map[string]string{"buildkit/createdat": string(createdAtText), "foo": "bar"},
			}, {
				Digest:      dgst("d1"),
				Annotations: map[string]string{"buildkit/createdat": before.Format(time.RFC3339Nano)},
			}},
		}
		foo, _, err := cc.Add(outputKey(dgst("foo"), 0), nil, nil)
		require.NoError(t, err)
		// built with append: a one-element literal makes gosec report G602 on deps[i][j] in Add
		var deps [][]solver.CacheLink
		deps = append(deps, []solver.CacheLink{{Src: foo}})
		_, _, err = cc.Add(outputKey(dgst("bar"), 0), deps, []solver.CacheExportResult{{
			CreatedAt: createdAt,
			Result:    r,
		}})
		require.NoError(t, err)
		_, _, err = cc.Add(outputKey(dgst("baz"), 0), nil, []solver.CacheExportResult{{
			CreatedAt: before,
			Result:    &solver.Remote{Descriptors: r.Descriptors[1:]},
		}})
		require.NoError(t, err)
		return cc, r
	}

	cc1, r1 := build(time.Now())
	ctx := WithSourceDateEpoch(t.Context(), &sde)
	cfg1, descs1, err := cc1.Marshal(ctx)
	require.NoError(t, err)

	for _, rec := range cfg1.Records {
		for _, res := range rec.Results {
			if rec.Digest == outputKey(dgst("baz"), 0) {
				require.Equal(t, before, res.CreatedAt, "timestamps before the epoch are kept")
			} else {
				require.Equal(t, sde, res.CreatedAt)
			}
		}
	}
	require.Equal(t, "1970-01-01T00:00:42Z", descs1[dgst("d0")].Descriptor.Annotations["buildkit/createdat"])
	require.Equal(t, "bar", descs1[dgst("d0")].Descriptor.Annotations["foo"])
	require.Equal(t, before.Format(time.RFC3339Nano), descs1[dgst("d1")].Descriptor.Annotations["buildkit/createdat"])
	require.NotEqual(t, "1970-01-01T00:00:42Z", r1.Descriptors[0].Annotations["buildkit/createdat"], "descriptors of the ref are not modified")

	// same cache created at another time marshals to the same config
	cc2, _ := build(time.Now().Add(time.Hour))
	cfg2, descs2, err := cc2.Marshal(ctx)
	require.NoError(t, err)
	dt1, err := json.Marshal(cfg1)
	require.NoError(t, err)
	dt2, err := json.Marshal(cfg2)
	require.NoError(t, err)
	require.Equal(t, string(dt1), string(dt2))
	require.Equal(t, descs1[dgst("d0")].Descriptor, descs2[dgst("d0")].Descriptor)

	// without an epoch, timestamps are kept
	cfg3, descs3, err := cc2.Marshal(t.Context())
	require.NoError(t, err)
	dt3, err := json.Marshal(cfg3)
	require.NoError(t, err)
	require.NotEqual(t, string(dt2), string(dt3))
	require.NotEqual(t, "1970-01-01T00:00:42Z", descs3[dgst("d0")].Descriptor.Annotations["buildkit/createdat"])
}
