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

func (c *kvCacheStorage) LoadWithParents(ctx context.Context, key *CacheKey, id string) ([]LoadedResult, error) {
	lwp, ok := c.results.(interface {
		LoadWithParents(context.Context, CacheResult) (map[string]Result, error)
	})
	if !ok {
		return nil, ErrNotImplemented
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	cr, err := c.backend.Load(key.ID, id)
	if err != nil {
		return nil, err
	}

	m, err := lwp.LoadWithParents(ctx, cr)
	if err != nil {
		return nil, err
	}

	results, err := c.filterResults(m, key, map[string]struct{}{})
	if err != nil {
		for _, r := range m {
			r.Release(context.TODO())
		}
	}
	for _, r := range m {
		// refs added to results are deleted from m by filterResults
		// so release any leftovers
		r.Release(context.TODO())
	}

	return results, nil
}

func (c *kvCacheStorage) filterResults(m map[string]Result, ck *CacheKey, visited map[string]struct{}) (results []LoadedResult, err error) {
	if _, ok := visited[ck.ID]; ok {
		return nil, nil
	}

	visited[ck.ID] = struct{}{}
	if err := c.backend.WalkResults(ck.ID, func(cr CacheResult) error {
		res, ok := m[ck.ID]
		if ok {
			results = append(results, LoadedResult{
				Result:      res,
				CacheKey:    ck,
				CacheResult: cr,
			})
			delete(m, ck.ID)
		}
		return nil
	}); err != nil {
		for _, r := range results {
			r.Result.Release(context.TODO())
		}
	}

	for _, keys := range ck.Deps() {
		for _, key := range keys {
			res, err := c.filterResults(m, key.CacheKey.CacheKey, visited)
			if err != nil {
				for _, r := range results {
					r.Result.Release(context.TODO())
				}
				return nil, err
			}
			results = append(results, res...)
		}
	}
	return
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
