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
	return c.storage.ReleaseUnreferenced(ctx)
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

	// Resolve the cache keys for all dependencies so the underlying
	// cache storage is only dealing with fresh cache keys.
	c.resolveDepKeys(deps)

	// Query the underlying storage.
	return c.query(deps, input, dgst, output)
}

func (c *cacheManager) query(deps []CacheKeyWithSelector, input Index, dgst digest.Digest, output Index) ([]*CacheKey, error) {
	keys, err := c.storage.Query(deps, input, dgst, output)
	if err != nil {
		return nil, err
	}

	for i, ck := range keys {
		if ck.equiv == nil {
			ck.equiv = map[*cacheManager]*CacheKey{}
		}
		keys[i].equiv[c] = ck
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

	outs, err := c.storage.Records(ctx, c.getKey(ck))
	if err != nil {
		return nil, err
	}

	// Set the associated owners for these cache records since the cache storage interface
	// doesn't have access to these.
	for _, r := range outs {
		r.cacheManager = c
		r.key = ck
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

	return c.storage.Load(ctx, c.getKey(rec.key), rec.ID)
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

func (c *cacheManager) getKey(k *CacheKey) *CacheKey {
	k.mu.Lock()
	key, ok := k.equiv[c]
	if ok {
		k.mu.Unlock()
		return key
	}
	if len(k.deps) == 0 {
		k.equiv[c] = k
		k.mu.Unlock()
		return k
	}
	key = c.getKeyFromDeps(k)
	k.equiv[c] = key
	k.mu.Unlock()
	return key
}

func (c *cacheManager) getID(k *CacheKey) string {
	return c.getKey(k).ID
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

	// Resolve the cache keys for the dependencies.
	for _, deps := range ck.deps {
		c.resolveDepKeys(deps)
	}

	// Cache keys for dependencies have been fully resolved so we can
	// send a request to the underlying storage.
	keys, err := c.query(ck.deps[0], 0, ck.Digest(), ck.Output())
	if err != nil {
		return withArbitraryIdentity(ck)
	}

	matches := map[string]struct{}{}
	for _, k := range keys {
		matches[k.ID] = struct{}{}
	}

	for i, deps := range ck.deps[1:] {
		if len(matches) == 0 {
			break
		}

		keys, err := c.query(deps, Index(i+1), ck.Digest(), ck.Output())

		// Return on error or short circuit if no keys were found.
		if err != nil || len(keys) == 0 {
			return withArbitraryIdentity(ck)
		}

		m2 := make(map[string]struct{}, len(keys))
		for _, k := range keys {
			m2[k.ID] = struct{}{}
		}

		for id := range matches {
			if _, ok := m2[id]; !ok {
				delete(matches, id)
			}
		}
	}

	for k := range matches {
		ck.ID = k
		return ck
	}

	// Unable to resolve the id to an existing one based
	// on the dependencies so generate a new id.
	return withArbitraryIdentity(ck)
}

// resolveDepKeys resolves the cache keys in the dependencies
// to ensure the id is set to one that makes sense for the cache
// manager it is attached to.
func (c *cacheManager) resolveDepKeys(deps []CacheKeyWithSelector) {
	for i, d := range deps {
		deps[i].CacheKey.CacheKey = c.getKey(d.CacheKey.CacheKey)
	}
}

func rootKey(dgst digest.Digest, output Index) digest.Digest {
	out, _ := cachedigest.FromBytes(fmt.Appendf(nil, "%s@%d", dgst, output), cachedigest.TypeString)
	if strings.HasPrefix(dgst.String(), "random:") {
		return digest.Digest("random:" + dgst.Encoded())
	}
	return out
}

func withArbitraryIdentity(ck *CacheKey) *CacheKey {
	ck.ID = identity.NewID()
	return ck
}
