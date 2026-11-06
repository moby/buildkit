package cacheimport

import (
	"encoding/json"
	"math/rand/v2"
	"testing"
	"time"

	cacheimporttypes "github.com/moby/buildkit/cache/remotecache/v1/types"
	"github.com/moby/buildkit/solver"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

// shuffleConfig returns a copy of cc with layers and records in a random
// order, all indexes being remapped accordingly.
func shuffleConfig(rng *rand.Rand, cc cacheimporttypes.CacheConfig) cacheimporttypes.CacheConfig {
	layerPerm := rng.Perm(len(cc.Layers))
	recordPerm := rng.Perm(len(cc.Records))

	var out cacheimporttypes.CacheConfig
	out.Layers = make([]cacheimporttypes.CacheLayer, len(cc.Layers))
	for oldIdx, l := range cc.Layers {
		if l.ParentIndex != -1 {
			l.ParentIndex = layerPerm[l.ParentIndex]
		}
		out.Layers[layerPerm[oldIdx]] = l
	}
	out.Records = make([]cacheimporttypes.CacheRecord, len(cc.Records))
	for oldIdx, r := range cc.Records {
		var nr cacheimporttypes.CacheRecord
		nr.Digest = r.Digest
		for _, res := range r.Results {
			res.LayerIndex = layerPerm[res.LayerIndex]
			nr.Results = append(nr.Results, res)
		}
		for _, in := range r.Inputs {
			var nin []cacheimporttypes.CacheInput
			for _, link := range rng.Perm(len(in)) {
				l := in[link]
				l.LinkIndex = recordPerm[l.LinkIndex]
				nin = append(nin, l)
			}
			nr.Inputs = append(nr.Inputs, nin)
		}
		out.Records[recordPerm[oldIdx]] = nr
	}
	return out
}

func marshalSorted(t *testing.T, cc cacheimporttypes.CacheConfig) string {
	t.Helper()
	sortConfig(&cc)
	dt, err := json.Marshal(cc)
	require.NoError(t, err)
	return string(dt)
}

func TestSortConfigIndependentOfInputOrder(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()
	cc := cacheimporttypes.CacheConfig{
		Layers: []cacheimporttypes.CacheLayer{
			{Blob: dgst("b0"), ParentIndex: -1}, // 0
			{Blob: dgst("b1"), ParentIndex: 0},  // 1: b0/b1
			{Blob: dgst("b1"), ParentIndex: -1}, // 2: b1, same blob as 1, different chain
		},
		Records: []cacheimporttypes.CacheRecord{
			{Digest: dgst("p")}, // 0
			{Digest: dgst("q")}, // 1
			// 2, 3: same digest, inputs differ by digest (already handled before)
			{Digest: dgst("x"), Inputs: [][]cacheimporttypes.CacheInput{{{LinkIndex: 0}}}},
			{Digest: dgst("x"), Inputs: [][]cacheimporttypes.CacheInput{{{LinkIndex: 1}}}},
			// 4, 5: same digest, inputs have the same digest but different
			// inputs themselves, and different results
			{Digest: dgst("a"), Inputs: [][]cacheimporttypes.CacheInput{{{LinkIndex: 2}}}, Results: []cacheimporttypes.CacheResult{{LayerIndex: 1, CreatedAt: now}}},
			{Digest: dgst("a"), Inputs: [][]cacheimporttypes.CacheInput{{{LinkIndex: 3}}}, Results: []cacheimporttypes.CacheResult{{LayerIndex: 2, CreatedAt: now}}},
			// 6, 7: same digest, no inputs, results only differ by layer chain
			{Digest: dgst("b"), Results: []cacheimporttypes.CacheResult{{LayerIndex: 1, CreatedAt: now}}},
			{Digest: dgst("b"), Results: []cacheimporttypes.CacheResult{{LayerIndex: 2, CreatedAt: now}}},
			// 8: several links in one input, with the same selector
			{Digest: dgst("c"), Inputs: [][]cacheimporttypes.CacheInput{{{Selector: "s", LinkIndex: 4}, {Selector: "s", LinkIndex: 5}, {Selector: "t", LinkIndex: 6}}}},
		},
	}

	first := rand.New(rand.NewPCG(0, 0)) //nolint:gosec // deterministic shuffling for the test
	rng := rand.New(rand.NewPCG(1, 2))   //nolint:gosec // deterministic shuffling for the test
	expected := marshalSorted(t, shuffleConfig(first, cc))
	for range 200 {
		require.Equal(t, expected, marshalSorted(t, shuffleConfig(rng, cc)))
	}

	var sorted cacheimporttypes.CacheConfig
	require.NoError(t, json.Unmarshal([]byte(expected), &sorted))
	require.Len(t, sorted.Layers, 3)
	require.Len(t, sorted.Records, 9)
}

func TestSortConfigMergesIdenticalRecords(t *testing.T) {
	cc := cacheimporttypes.CacheConfig{
		Layers: []cacheimporttypes.CacheLayer{{Blob: dgst("b0"), ParentIndex: -1}},
		Records: []cacheimporttypes.CacheRecord{
			{Digest: dgst("r")}, // 0
			{Digest: dgst("r")}, // 1: identical to 0
			{Digest: dgst("c"), Inputs: [][]cacheimporttypes.CacheInput{{{LinkIndex: 0}}}, Results: []cacheimporttypes.CacheResult{{LayerIndex: 0}}},
			{Digest: dgst("c"), Inputs: [][]cacheimporttypes.CacheInput{{{LinkIndex: 1}}}, Results: []cacheimporttypes.CacheResult{{LayerIndex: 0}}},
			{Digest: dgst("d"), Inputs: [][]cacheimporttypes.CacheInput{{{LinkIndex: 0}, {LinkIndex: 1}}}},
		},
	}

	expected := marshalSorted(t, cc)
	rng := rand.New(rand.NewPCG(3, 4)) //nolint:gosec // deterministic shuffling for the test
	for range 50 {
		require.Equal(t, expected, marshalSorted(t, shuffleConfig(rng, cc)))
	}

	var sorted cacheimporttypes.CacheConfig
	require.NoError(t, json.Unmarshal([]byte(expected), &sorted))
	require.Len(t, sorted.Records, 3)
	for _, r := range sorted.Records {
		if r.Digest == dgst("d") {
			require.Len(t, r.Inputs, 1)
			require.Len(t, r.Inputs[0], 1)
		}
	}
}

// TestMarshalDeterministicSameDigestRecords reproduces records sharing a
// digest whose inputs also share a digest but have different inputs
// themselves: their order used to follow map iteration order.
func TestMarshalDeterministicSameDigestRecords(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()
	remote := func(names ...string) *solver.Remote {
		r := &solver.Remote{}
		for _, n := range names {
			r.Descriptors = append(r.Descriptors, ocispecs.Descriptor{Digest: dgst(n)})
		}
		return r
	}

	var expected string
	for i := range 50 {
		cc := NewCacheChains()
		p, _, err := cc.Add(outputKey(dgst("p"), 0), nil, nil)
		require.NoError(t, err)
		q, _, err := cc.Add(outputKey(dgst("q"), 0), nil, nil)
		require.NoError(t, err)
		x1, _, err := cc.Add(outputKey(dgst("x"), 0), singleDep(p), nil)
		require.NoError(t, err)
		x2, _, err := cc.Add(outputKey(dgst("x"), 0), singleDep(q), nil)
		require.NoError(t, err)
		_, _, err = cc.Add(outputKey(dgst("a"), 0), singleDep(x1), []solver.CacheExportResult{{CreatedAt: now, Result: remote("l0", "l1")}})
		require.NoError(t, err)
		_, _, err = cc.Add(outputKey(dgst("a"), 0), singleDep(x2), []solver.CacheExportResult{{CreatedAt: now, Result: remote("l0", "l2")}})
		require.NoError(t, err)

		cfg, _, err := cc.Marshal(t.Context())
		require.NoError(t, err)
		require.Len(t, cfg.Records, 6)
		dt, err := json.Marshal(cfg)
		require.NoError(t, err)
		if i == 0 {
			expected = string(dt)
			continue
		}
		require.Equal(t, expected, string(dt), "attempt %d", i)
	}
}

func TestBestResultDeterministic(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()
	r1 := solver.CacheExportResult{CreatedAt: now, Result: &solver.Remote{Descriptors: []ocispecs.Descriptor{{Digest: dgst("l1")}}}}
	r2 := solver.CacheExportResult{CreatedAt: now, Result: &solver.Remote{Descriptors: []ocispecs.Descriptor{{Digest: dgst("l2")}}}}
	older := solver.CacheExportResult{CreatedAt: now.Add(-time.Hour), Result: &solver.Remote{}}

	a := &item{results: []solver.CacheExportResult{older, r1, r2}}
	b := &item{results: []solver.CacheExportResult{r2, older, r1}}
	require.Equal(t, a.bestResult().Result.Descriptors, b.bestResult().Result.Descriptors)
}

// singleDep returns deps with a single input linked to src. Built with append:
// a one-element literal makes gosec report G602 on deps[i][j] in Add.
func singleDep(src solver.CacheExporterRecord) [][]solver.CacheLink {
	var deps [][]solver.CacheLink
	return append(deps, []solver.CacheLink{{Src: src}})
}
