package solver

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/util/bklog"
	"github.com/moby/buildkit/util/cachedigest"
	digest "github.com/opencontainers/go-digest"
	"github.com/sirupsen/logrus"
)

// NewInMemoryCacheManager creates a new in-memory cache manager
func NewInMemoryCacheManager() CacheManager {
	return NewCacheManager(context.TODO(), identity.NewID(), NewInMemoryCacheStorage(), NewInMemoryResultStorage())
}

// NewCacheManager creates a new cache manager with specific storage backend
func NewCacheManager(ctx context.Context, id string, storage CacheKeyStorage, results CacheResultStorage) CacheManager {
	cm := &cacheManager{
		id:      id,
		storage: newKvCacheStorage(storage, results),
	}

	if err := cm.ReleaseUnreferenced(ctx); err != nil {
		bklog.G(ctx).Errorf("failed to release unreferenced cache metadata: %+v", err)
	}

	return cm
}

type cacheManager struct {
	mu sync.RWMutex
	id string

	storage *kvCacheStorage
}

func (c *cacheManager) ReleaseUnreferenced(ctx context.Context) error {
	visited := map[string]struct{}{}
	return c.storage.backend.Walk(func(id string) error {
		return c.storage.backend.WalkResults(id, func(cr CacheResult) error {
			if _, ok := visited[cr.ID]; ok {
				return nil
			}
			visited[cr.ID] = struct{}{}
			if !c.storage.results.Exists(ctx, cr.ID) {
				c.storage.backend.Release(cr.ID)
			}
			return nil
		})
	})
}

func (c *cacheManager) ID() string {
	return c.id
}

func (c *cacheManager) Query(deps []CacheKeyWithSelector, input Index, dgst digest.Digest, output Index) (rcks []*CacheKey, rerr error) {
	depsField := make([]map[string]any, len(deps))
	for i, dep := range deps {
		depsField[i] = dep.TraceFields()
	}
	lg := bklog.G(context.TODO()).WithFields(logrus.Fields{
		"cache_manager": c.id,
		"op":            "query",
		"deps":          depsField,
		"input":         input,
		"digest":        dgst,
		"output":        output,
		"stack":         bklog.TraceLevelOnlyStack(),
	})
	defer func() {
		rcksField := make([]map[string]any, len(rcks))
		for i, rck := range rcks {
			rcksField[i] = rck.TraceFields()
		}
		lg.WithError(rerr).WithField("return_cachekeys", rcksField).Trace("cache manager")
	}()

	c.mu.RLock()
	defer c.mu.RUnlock()

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
		if err := c.storage.backend.WalkLinks(c.getID(d.key.CacheKey.CacheKey), CacheInfoLink{input, output, dgst, d.key.Selector}, func(id string) error {
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
	for id, key := range allRes {
		for _, d := range allDeps {
			if _, ok := d.results[id]; !ok {
				if err := c.storage.backend.AddLink(c.getID(d.key.CacheKey.CacheKey), CacheInfoLink{
					Input:    input,
					Output:   output,
					Digest:   dgst,
					Selector: d.key.Selector,
				}, c.getID(key)); err != nil {
					return nil, err
				}
			}
		}
	}

	if len(deps) == 0 {
		if !c.storage.backend.Exists(rootKey(dgst, output).String()) {
			return nil, nil
		}
		return []*CacheKey{c.newRootKey(dgst, output)}, nil
	}

	keys := make([]*CacheKey, 0, len(deps))
	for _, k := range allRes {
		keys = append(keys, k)
	}
	return keys, nil
}

func (c *cacheManager) Records(ctx context.Context, ck *CacheKey) (rrecs []*CacheRecord, rerr error) {
	lg := bklog.G(context.TODO()).WithFields(logrus.Fields{
		"cache_manager": c.id,
		"op":            "records",
		"cachekey":      ck.TraceFields(),
		"stack":         bklog.TraceLevelOnlyStack(),
	})
	defer func() {
		rrercsField := make([]map[string]any, len(rrecs))
		for i, rrec := range rrecs {
			rrercsField[i] = rrec.TraceFields()
		}
		lg.WithError(rerr).WithField("return_records", rrercsField).Trace("cache manager")
	}()

	outs := make([]*CacheRecord, 0)
	if err := c.storage.backend.WalkResults(c.getID(ck), func(r CacheResult) error {
		if c.storage.results.Exists(ctx, r.ID) {
			outs = append(outs, &CacheRecord{
				ID:           r.ID,
				cacheManager: c,
				key:          ck,
				CreatedAt:    r.CreatedAt,
			})
		} else {
			c.storage.backend.Release(r.ID)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return outs, nil
}

func (c *cacheManager) Load(ctx context.Context, rec *CacheRecord) (rres Result, rerr error) {
	lg := bklog.G(context.TODO()).WithFields(logrus.Fields{
		"cache_manager": c.id,
		"op":            "load",
		"record":        rec.TraceFields(),
		"stack":         bklog.TraceLevelOnlyStack(),
	})
	defer func() {
		rresID := "<nil>"
		if rres != nil {
			rresID = rres.ID()
		}
		lg.WithError(rerr).WithField("return_result", rresID).Trace("cache manager")
	}()

	c.mu.RLock()
	defer c.mu.RUnlock()

	res, err := c.storage.backend.Load(c.getID(rec.key), rec.ID)
	if err != nil {
		return nil, err
	}

	return c.storage.results.Load(ctx, res)
}

type LoadedResult struct {
	Result      Result
	CacheResult CacheResult
	CacheKey    *CacheKey
}

func (r *LoadedResult) TraceFields() map[string]any {
	return map[string]any{
		"result":       r.Result.ID(),
		"cache_result": r.CacheResult.ID,
		"cache_key":    r.CacheKey.TraceFields(),
	}
}

func (c *cacheManager) filterResults(m map[string]Result, ck *CacheKey, visited map[string]struct{}) (results []LoadedResult, err error) {
	id := c.getID(ck)
	if _, ok := visited[id]; ok {
		return nil, nil
	}
	visited[id] = struct{}{}
	if err := c.storage.backend.WalkResults(id, func(cr CacheResult) error {
		res, ok := m[id]
		if ok {
			results = append(results, LoadedResult{
				Result:      res,
				CacheKey:    ck,
				CacheResult: cr,
			})
			delete(m, id)
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

func (c *cacheManager) LoadWithParents(ctx context.Context, rec *CacheRecord) (rres []LoadedResult, rerr error) {
	lg := bklog.G(context.TODO()).WithFields(logrus.Fields{
		"cache_manager": c.id,
		"op":            "load_with_parents",
		"record":        rec.TraceFields(),
		"stack":         bklog.TraceLevelOnlyStack(),
	})
	defer func() {
		rresField := make([]map[string]any, len(rres))
		for i, rres := range rres {
			rresField[i] = rres.TraceFields()
		}
		lg.WithError(rerr).WithField("return_results", rresField).Trace("cache manager")
	}()

	lwp, ok := c.storage.results.(interface {
		LoadWithParents(context.Context, CacheResult) (map[string]Result, error)
	})
	if !ok {
		res, err := c.Load(ctx, rec)
		if err != nil {
			return nil, err
		}
		return []LoadedResult{{Result: res, CacheKey: rec.key, CacheResult: CacheResult{ID: c.getID(rec.key), CreatedAt: rec.CreatedAt}}}, nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()

	cr, err := c.storage.backend.Load(c.getID(rec.key), rec.ID)
	if err != nil {
		return nil, err
	}

	m, err := lwp.LoadWithParents(ctx, cr)
	if err != nil {
		return nil, err
	}

	results, err := c.filterResults(m, rec.key, map[string]struct{}{})
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

func (c *cacheManager) Save(k *CacheKey, r Result, createdAt time.Time) (rck *ExportableCacheKey, err error) {
	lg := bklog.G(context.TODO()).WithFields(logrus.Fields{
		"cache_manager": c.id,
		"op":            "save",
		"result":        r.ID(),
		"stack":         bklog.TraceLevelOnlyStack(),
	})
	defer func() {
		if err != nil {
			lg = lg.WithError(err)
		} else {
			lg = lg.WithField("return_cachekey", rck.TraceFields())
		}
		lg.Trace("cache manager")
	}()

	c.mu.Lock()
	defer c.mu.Unlock()

	res, err := c.storage.results.Save(r, createdAt)
	if err != nil {
		return nil, err
	}
	if err := c.storage.backend.AddResult(c.getID(k), res); err != nil {
		return nil, err
	}

	if err := c.ensurePersistentKey(k); err != nil {
		return nil, err
	}

	rec := &CacheRecord{
		ID:           res.ID,
		cacheManager: c,
		key:          k,
		CreatedAt:    res.CreatedAt,
	}

	return &ExportableCacheKey{
		CacheKey: k,
		Exporter: &exporter{k: k, record: rec},
	}, nil
}

func newKey() *CacheKey {
	return &CacheKey{equiv: map[*cacheManager]*CacheKey{}}
}

func (c *cacheManager) newKeyWithID(id string, dgst digest.Digest, output Index) *CacheKey {
	k := newKey()
	k.digest = dgst
	k.output = output
	k.ID = id
	k.equiv[c] = k
	return k
}

func (c *cacheManager) newRootKey(dgst digest.Digest, output Index) *CacheKey {
	return c.newKeyWithID(rootKey(dgst, output).String(), dgst, output)
}

func (c *cacheManager) getID(k *CacheKey) string {
	k.mu.Lock()
	key, ok := k.equiv[c]
	if ok {
		k.mu.Unlock()
		return key.ID
	}
	if len(k.deps) == 0 {
		k.equiv[c] = k
		k.mu.Unlock()
		return k.ID
	}
	key = c.getKeyFromDeps(k)
	k.equiv[c] = key
	k.mu.Unlock()
	return key.ID
}

func (c *cacheManager) ensurePersistentKey(k *CacheKey) error {
	id := c.getID(k)
	for i, deps := range k.Deps() {
		for _, ck := range deps {
			l := CacheInfoLink{
				Input:    Index(i),
				Output:   k.Output(),
				Digest:   k.Digest(),
				Selector: ck.Selector,
			}
			ckID := c.getID(ck.CacheKey.CacheKey)
			if !c.storage.backend.HasLink(ckID, l, id) {
				if err := c.ensurePersistentKey(ck.CacheKey.CacheKey); err != nil {
					return err
				}
				if err := c.storage.backend.AddLink(ckID, l, id); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (c *cacheManager) getKeyFromDeps(k *CacheKey) (ck *CacheKey) {
	ck = k
	if len(k.equiv) > 0 {
		// Cannot reuse this cache key since it is already in use
		// by a separate cache manager so duplicate the contents so
		// we can resolve the dependencies in relation to this cache
		// manager.
		ck = &CacheKey{
			digest: k.digest,
			vtx:    k.vtx,
			output: k.output,
			equiv:  map[*cacheManager]*CacheKey{},
			deps:   make([][]CacheKeyWithSelector, len(k.deps)),
		}

		// Duplicate the dependency slice so we can feel free to modify it
		// freely.
		for i, dep := range k.deps {
			ck.deps[i] = slices.Clone(dep)
		}
		ck.equiv[c] = ck
	}

	matches := map[string]struct{}{}
	for i, deps := range k.deps {
		if i == 0 || len(matches) > 0 {
			for _, ck := range deps {
				m2 := make(map[string]struct{})
				if err := c.storage.backend.WalkLinks(c.getID(ck.CacheKey.CacheKey), CacheInfoLink{
					Input:    Index(i),
					Output:   k.Output(),
					Digest:   k.Digest(),
					Selector: ck.Selector,
				}, func(id string) error {
					if i == 0 {
						matches[id] = struct{}{}
					} else {
						m2[id] = struct{}{}
					}
					return nil
				}); err != nil {
					matches = map[string]struct{}{}
					break
				}
				if i != 0 {
					for id := range matches {
						if _, ok := m2[id]; !ok {
							delete(matches, id)
						}
					}
				}
			}
		}
	}

	for k := range matches {
		ck.ID = k
		return ck
	}

	// Unable to resolve the id to an existing one based
	// on the dependencies so generate a new id.
	ck.ID = identity.NewID()
	return ck
}

func rootKey(dgst digest.Digest, output Index) digest.Digest {
	out, _ := cachedigest.FromBytes(fmt.Appendf(nil, "%s@%d", dgst, output), cachedigest.TypeString)
	if strings.HasPrefix(dgst.String(), "random:") {
		return digest.Digest("random:" + dgst.Encoded())
	}
	return out
}
