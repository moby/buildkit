package solver

import (
	"context"
	"maps"
	"slices"
	"sync"

	digest "github.com/opencontainers/go-digest"
)

var _ CacheStorage = (*kvCacheStorage)(nil)

type kvCacheStorage struct {
	mu sync.RWMutex

	backend CacheKeyStorage
	results CacheResultStorage
}

func newKvCacheStorage(backend CacheKeyStorage, results CacheResultStorage) *kvCacheStorage {
	return &kvCacheStorage{
		backend: backend,
		results: results,
	}
}

func (c *kvCacheStorage) Query(deps []CacheKeyWithSelector, input Index, dgst digest.Digest, output Index) ([]*CacheKey, error) {
	id := rootKey(dgst, output).String()

	c.mu.RLock()
	defer c.mu.RUnlock()

	if len(deps) == 0 {
		if !c.backend.Exists(id) {
			return nil, nil
		}
		return []*CacheKey{c.newKeyWithID(id, dgst, output)}, nil
	}

	type dep struct {
		results map[string]struct{}
		key     CacheKeyWithSelector
	}

	allDeps := make([]dep, 0, len(deps))
	for _, k := range deps {
		allDeps = append(allDeps, dep{key: k, results: map[string]struct{}{}})
	}

	allRes := map[string]*CacheKey{}
	for _, d := range allDeps {
		if err := c.backend.WalkLinks(d.key.CacheKey.ID, CacheInfoLink{input, output, dgst, d.key.Selector}, func(id string) error {
			d.results[id] = struct{}{}
			if _, ok := allRes[id]; !ok {
				allRes[id] = c.newKeyWithID(id, dgst, output)
			}
			return nil
		}); err != nil {
			return nil, err
		}
	}

	// link the results against the keys that didn't exist
	// TODO: why is this here and what exactly is this doing?
	for id, key := range allRes {
		for _, d := range allDeps {
			if _, ok := d.results[id]; !ok {
				if err := c.backend.AddLink(d.key.CacheKey.ID, CacheInfoLink{
					Input:    input,
					Output:   output,
					Digest:   dgst,
					Selector: d.key.Selector,
				}, key.ID); err != nil {
					return nil, err
				}
			}
		}
	}

	keys := slices.Collect(maps.Values(allRes))
	return keys, nil
}

func (c *kvCacheStorage) newKeyWithID(id string, dgst digest.Digest, output Index) *CacheKey {
	k := newKey()
	k.digest = dgst
	k.output = output
	k.ID = id
	return k
}

func (c *kvCacheStorage) Records(ctx context.Context, ck *CacheKey) ([]*CacheRecord, error) {
	outs := make([]*CacheRecord, 0)
	if err := c.backend.WalkResults(ck.ID, func(r CacheResult) error {
		if c.results.Exists(ctx, r.ID) {
			outs = append(outs, &CacheRecord{
				ID:        r.ID,
				CreatedAt: r.CreatedAt,
			})
		} else {
			c.backend.Release(r.ID)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return outs, nil
}

func (c *kvCacheStorage) Load(ctx context.Context, key *CacheKey, id string) (Result, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	res, err := c.backend.Load(key.ID, id)
	if err != nil {
		return nil, err
	}
	return c.results.Load(ctx, res)
}

func (c *kvCacheStorage) ReleaseUnreferenced(ctx context.Context) error {
	visited := map[string]struct{}{}
	return c.backend.Walk(func(id string) error {
		return c.backend.WalkResults(id, func(cr CacheResult) error {
			if _, ok := visited[cr.ID]; ok {
				return nil
			}
			visited[cr.ID] = struct{}{}
			if !c.results.Exists(ctx, cr.ID) {
				c.backend.Release(cr.ID)
			}
			return nil
		})
	})
}
