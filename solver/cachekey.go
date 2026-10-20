package solver

import (
	"maps"
	"slices"
	"sync"

	digest "github.com/opencontainers/go-digest"
)

// NewCacheKey creates a new cache key for a specific output index
func NewCacheKey(dgst, vtx digest.Digest, output Index) *CacheKey {
	return &CacheKey{
		ID:     rootKey(dgst, output).String(),
		digest: dgst,
		vtx:    vtx,
		output: output,
		ids:    map[*cacheManager]string{},
	}
}

// CacheKeyWithSelector combines a cache key with an optional selector digest.
// Used to limit the matches for dependency cache key.
type CacheKeyWithSelector struct {
	Selector digest.Digest
	CacheKey ExportableCacheKey
}

func (ck CacheKeyWithSelector) TraceFields() map[string]any {
	fields := ck.CacheKey.TraceFields()
	fields["selector"] = ck.Selector.String()
	return fields
}

type CacheKey struct {
	mu sync.RWMutex

	ID   string
	deps [][]CacheKeyWithSelector
	// digest is the digest returned by the CacheMap implementation of this op
	digest digest.Digest
	// vtx is the LLB digest that this op was created for
	vtx    digest.Digest
	output Index
	ids    map[*cacheManager]string

	indexIDs []string
}

func (ck *CacheKey) TraceFields() map[string]any {
	ck.mu.RLock()
	defer ck.mu.RUnlock()
	idsMap := map[string]string{}
	for cm, id := range ck.ids {
		idsMap[cm.ID()] = id
	}

	// don't recurse more than one level in showing deps
	depsMap := make([]map[string]string, len(ck.deps))
	for i, deps := range ck.deps {
		depsMap[i] = map[string]string{}
		for _, ck := range deps {
			depsMap[i]["id"] = ck.CacheKey.ID
			depsMap[i]["selector"] = ck.Selector.String()
		}
	}

	return map[string]any{
		"id":       ck.ID,
		"digest":   ck.digest,
		"vtx":      ck.vtx,
		"output":   ck.output,
		"indexIDs": ck.indexIDs,
		"ids":      idsMap,
		"deps":     depsMap,
	}
}

func (ck *CacheKey) Deps() [][]CacheKeyWithSelector {
	ck.mu.RLock()
	defer ck.mu.RUnlock()
	deps := make([][]CacheKeyWithSelector, len(ck.deps))
	for i := range ck.deps {
		deps[i] = slices.Clone(ck.deps[i])
	}
	return deps
}

func (ck *CacheKey) Digest() digest.Digest {
	return ck.digest
}
func (ck *CacheKey) Output() Index {
	return ck.output
}

func (ck *CacheKey) clone() *CacheKey {
	ck.mu.RLock()
	nk := &CacheKey{
		ID:     ck.ID,
		digest: ck.digest,
		vtx:    ck.vtx,
		output: ck.output,
		ids:    make(map[*cacheManager]string, len(ck.ids)),
	}
	maps.Copy(nk.ids, ck.ids)
	ck.mu.RUnlock()
	return nk
}

// cloneCacheKeysForQuery snapshots mutable key metadata before a query runs
// outside the scheduler lock.
func cloneCacheKeysForQuery(keys []CacheKeyWithSelector) []CacheKeyWithSelector {
	cloned := make(map[*CacheKey]*CacheKey)
	var cloneKey func(*CacheKey) *CacheKey
	cloneKey = func(ck *CacheKey) *CacheKey {
		if ck == nil {
			return nil
		}
		if nk, ok := cloned[ck]; ok {
			return nk
		}

		ck.mu.RLock()
		nk := &CacheKey{
			ID:       ck.ID,
			digest:   ck.digest,
			vtx:      ck.vtx,
			output:   ck.output,
			ids:      make(map[*cacheManager]string, len(ck.ids)),
			indexIDs: slices.Clone(ck.indexIDs),
		}
		maps.Copy(nk.ids, ck.ids)
		cloned[ck] = nk
		nk.deps = make([][]CacheKeyWithSelector, len(ck.deps))
		for i, deps := range ck.deps {
			nk.deps[i] = make([]CacheKeyWithSelector, len(deps))
			for j, dep := range deps {
				nk.deps[i][j] = dep
				nk.deps[i][j].CacheKey.CacheKey = cloneKey(dep.CacheKey.CacheKey)
			}
		}
		ck.mu.RUnlock()
		return nk
	}

	out := make([]CacheKeyWithSelector, len(keys))
	for i, key := range keys {
		out[i] = key
		out[i].CacheKey.CacheKey = cloneKey(key.CacheKey.CacheKey)
	}
	return out
}
