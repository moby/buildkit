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

func TestAddResultDedupe(t *testing.T) {
	now := time.Now()
	remote := func(names ...string) *solver.Remote {
		r := &solver.Remote{}
		for _, n := range names {
			r.Descriptors = append(r.Descriptors, ocispecs.Descriptor{Digest: dgst(n)})
		}
		return r
	}
	it := &item{}
	it.addResult(solver.CacheExportResult{CreatedAt: now, Result: remote("a", "b")})
	it.addResult(solver.CacheExportResult{CreatedAt: now, Result: remote("a", "b")})
	require.Len(t, it.results, 1, "identical results are deduplicated")

	it.addResult(solver.CacheExportResult{CreatedAt: now, Result: remote("a", "c")})
	require.Len(t, it.results, 2, "results with different layers created at the same time are kept")

	it.addResult(solver.CacheExportResult{CreatedAt: now.Add(time.Second), Result: remote("a", "b")})
	require.Len(t, it.results, 3)
}
