package solver

import (
	"maps"
	"testing"
	"time"

	"github.com/moby/buildkit/identity"
	digest "github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
)

// TestMultipleCacheSourcesSharedChain solves a chain into two cache sources
// that share record IDs, adds the dependent record to the second only, then
// solves the dependent with both sources imported and the first answering
// first. The dependent must load from the second source.
func TestMultipleCacheSourcesSharedChain(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	results := NewInMemoryResultStorage()

	newBase := func(cache []CacheManager) Edge {
		return Edge{Vertex: vtx(vtxOpt{
			name:         "base",
			cacheKeySeed: "seedbase",
			value:        "base",
			cacheSources: cache,
		})}
	}
	newCtx := func(cache []CacheManager) Edge {
		return Edge{Vertex: vtx(vtxOpt{
			name:         "ctx",
			cacheKeySeed: identity.NewID(),
			value:        "ctx-content",
			cacheSources: cache,
		})}
	}
	newShared := func(value string, cache []CacheManager) Edge {
		return Edge{Vertex: vtx(vtxOpt{
			name:         "v1",
			cacheKeySeed: "seed1",
			value:        value,
			inputs:       []Edge{newBase(cache), newCtx(cache)},
			slowCacheCompute: map[int]ResultBasedCacheFunc{
				1: digestFromResult,
			},
			cacheSources: cache,
		})}
	}
	newDependent := func(value, sharedValue string, cache []CacheManager) Edge {
		return Edge{Vertex: vtx(vtxOpt{
			name:         "v0",
			cacheKeySeed: "seed0",
			value:        value,
			inputs:       []Edge{newShared(sharedValue, cache)},
			cacheSources: cache,
		})}
	}

	solve := func(name string, main CacheManager, g Edge) string {
		l := NewSolver(SolverOpt{
			ResolveOpFunc: testOpResolver,
			DefaultCache:  main,
		})
		defer l.Close()
		j, err := l.NewJob(name)
		require.NoError(t, err)
		defer j.Discard()
		res, err := j.Build(ctx, g)
		require.NoError(t, err)
		return unwrap(res)
	}

	storeA := NewInMemoryCacheStorage()
	require.Equal(t, "result1", solve("a", NewCacheManager(ctx, "a-writer", storeA, results), newShared("result1", nil)))

	storeB := cloneInMemoryStore(storeA.(*inMemoryStore))
	require.Equal(t, "result0", solve("b", NewCacheManager(ctx, "b-writer", storeB, results), newDependent("result0", "not-cached", nil)))

	imports := []CacheManager{
		NewCacheManager(ctx, "A", readOnlyCacheKeyStorage{storeA}, results),
		delayedCacheManager{NewCacheManager(ctx, "B", readOnlyCacheKeyStorage{storeB}, results), 50 * time.Millisecond},
	}
	require.Equal(t, "result0", solve("c", NewInMemoryCacheManager(), newDependent("not-cached", "not-cached", imports)))
}

// cloneInMemoryStore copies a store so two managers hold the same record IDs
func cloneInMemoryStore(src *inMemoryStore) *inMemoryStore {
	src.mu.RLock()
	defer src.mu.RUnlock()
	dst := NewInMemoryCacheStorage().(*inMemoryStore)
	for id, k := range src.byID {
		nk := newInMemoryKey(id)
		maps.Copy(nk.results, k.results)
		for l, targets := range k.links {
			nk.links[l] = maps.Clone(targets)
		}
		nk.backlinks = maps.Clone(k.backlinks)
		dst.byID[id] = nk
	}
	for rid, ids := range src.byResult {
		dst.byResult[rid] = maps.Clone(ids)
	}
	return dst
}

// readOnlyCacheKeyStorage ignores writes, like an imported cache manifest does
type readOnlyCacheKeyStorage struct {
	CacheKeyStorage
}

func (readOnlyCacheKeyStorage) AddResult(string, CacheResult) error         { return nil }
func (readOnlyCacheKeyStorage) Release(string) error                        { return nil }
func (readOnlyCacheKeyStorage) AddLink(string, CacheInfoLink, string) error { return nil }

// delayedCacheManager makes the wrapped manager answer combined queries last
type delayedCacheManager struct {
	CacheManager
	delay time.Duration
}

func (d delayedCacheManager) Query(inp []CacheKeyWithSelector, inputIndex Index, dgst digest.Digest, outputIndex Index) ([]*CacheKey, error) {
	time.Sleep(d.delay)
	return d.CacheManager.Query(inp, inputIndex, dgst, outputIndex)
}
