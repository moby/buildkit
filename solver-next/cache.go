package solver

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/moby/buildkit/identity"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"golang.org/x/sync/errgroup"
)

type internalMemoryKeyT string

var internalMemoryKey = internalMemoryKeyT("buildkit/memory-cache-id")

var NoSelector = digest.FromBytes(nil)

func NewInMemoryCacheManager() CacheManager {
	return &inMemoryCacheManager{
		id:      identity.NewID(),
		backend: NewInMemoryCacheStorage(),
		results: NewInMemoryResultStorage(),
	}
}

type inMemoryCacheKey struct {
	CacheKeyInfo CacheKeyInfo
	manager      *inMemoryCacheManager
	cacheResult  CacheResult
	deps         []CacheKeyWithSelector // only []*inMemoryCacheKey
	CacheKey
}

func (ck *inMemoryCacheKey) Deps() []CacheKeyWithSelector {
	if len(ck.deps) == 0 || len(ck.CacheKeyInfo.Deps) > 0 {
		deps := make([]CacheKeyWithSelector, len(ck.CacheKeyInfo.Deps))
		for i, dep := range ck.CacheKeyInfo.Deps {
			k, err := ck.manager.backend.Get(dep.ID)
			if err != nil {
				logrus.Errorf("dependency %s not found", dep.ID)
			} else {
				deps[i] = CacheKeyWithSelector{
					CacheKey: withExporter(ck.manager.toInMemoryCacheKey(k), nil),
					Selector: dep.Selector,
				}
			}
		}
		ck.deps = deps
	}
	return ck.deps
}

func (ck *inMemoryCacheKey) Digest() digest.Digest {
	return ck.CacheKeyInfo.Base
}
func (ck *inMemoryCacheKey) Output() Index {
	return Index(ck.CacheKeyInfo.Output)
}

func withExporter(ck *inMemoryCacheKey, cacheResult *CacheResult) ExportableCacheKey {
	return ExportableCacheKey{ck, &cacheExporter{
		inMemoryCacheKey: ck,
		cacheResult:      cacheResult,
	}}
}

type cacheExporter struct {
	*inMemoryCacheKey
	cacheResult *CacheResult
}

func (ce *cacheExporter) Export(ctx context.Context, m map[digest.Digest]*ExportRecord, converter func(context.Context, Result) (*Remote, error)) (*ExportRecord, error) {
	var res Result
	if ce.cacheResult == nil {
		cr, err := ce.inMemoryCacheKey.manager.getBestResult(ce.inMemoryCacheKey.CacheKeyInfo)
		if err != nil {
			return nil, err
		}
		ce.cacheResult = cr
	}

	var remote *Remote
	var err error

	if ce.cacheResult != nil {
		remote, err = ce.inMemoryCacheKey.manager.results.LoadRemote(ctx, *ce.cacheResult)
		if err != nil {
			return nil, err
		}

		if remote == nil {
			res, err = ce.inMemoryCacheKey.manager.results.Load(ctx, *ce.cacheResult)
			if err != nil {
				return nil, err
			}
		}
	}

	if res != nil && remote == nil {
		remote, err = converter(ctx, res)
		if err != nil {
			return nil, err
		}
	}

	cacheID := digest.FromBytes([]byte(ce.inMemoryCacheKey.CacheKeyInfo.ID))
	if remote != nil && len(remote.Descriptors) > 0 && remote.Descriptors[0].Digest != "" {
		cacheID = remote.Descriptors[0].Digest
	}

	deps := ce.deps

	rec, ok := m[cacheID]
	if !ok {
		rec = &ExportRecord{
			Digest: cacheID,
			Remote: remote,
			Links:  make(map[CacheLink]struct{}),
		}
		m[cacheID] = rec
	}

	if len(deps) == 0 {
		rec.Links[CacheLink{
			Output: ce.Output(),
			Base:   ce.Digest(),
		}] = struct{}{}
	}

	for i, dep := range ce.Deps() {
		r, err := dep.CacheKey.Export(ctx, m, converter)
		if err != nil {
			return nil, err
		}
		link := CacheLink{
			Source:   r.Digest,
			Input:    Index(i),
			Output:   ce.Output(),
			Base:     ce.Digest(),
			Selector: dep.Selector,
		}
		rec.Links[link] = struct{}{}
	}

	return rec, nil
}

type inMemoryCacheManager struct {
	mu sync.RWMutex
	id string

	backend CacheKeyStorage
	results CacheResultStorage
}

func (c *inMemoryCacheManager) ID() string {
	return c.id
}

func (c *inMemoryCacheManager) toInMemoryCacheKey(cki CacheKeyInfo) *inMemoryCacheKey {
	return &inMemoryCacheKey{
		CacheKeyInfo: cki,
		manager:      c,
		CacheKey:     NewCacheKey("", 0, nil),
	}
}

func (c *inMemoryCacheManager) getBestResult(cki CacheKeyInfo) (*CacheResult, error) {
	var results []*CacheResult
	if err := c.backend.WalkResults(cki.ID, func(res CacheResult) error {
		results = append(results, &res)
		return nil
	}); err != nil {
		return nil, err
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].CreatedAt.Before(results[j].CreatedAt)
	})

	if len(results) > 0 {
		return results[0], nil
	}

	return nil, nil
}

func (c *inMemoryCacheManager) Query(deps []ExportableCacheKey, input Index, dgst digest.Digest, output Index, selector digest.Digest) ([]*CacheRecord, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	refs := map[string]struct{}{}
	sublinks := map[string]struct{}{}

	for _, dep := range deps {
		ck, err := c.getInternalKey(dep, false)
		if err == nil {
			if err := c.backend.WalkLinks(ck.CacheKeyInfo.ID, CacheInfoLink{input, output, dgst, selector}, func(id string) error {
				refs[id] = struct{}{}
				return nil
			}); err != nil {
				return nil, err
			}

			if err := c.backend.WalkLinks(ck.CacheKeyInfo.ID, CacheInfoLink{Index(-1), Index(0), "", selector}, func(id string) error {
				sublinks[id] = struct{}{}
				return nil
			}); err != nil {
				return nil, err
			}

			if err := c.backend.WalkLinks(ck.CacheKeyInfo.ID, CacheInfoLink{Index(-1), Index(0), "", NoSelector}, func(id string) error {
				sublinks[id] = struct{}{}
				return nil
			}); err != nil {
				return nil, err
			}
		}
	}

	for id := range sublinks {
		ck, err := c.backend.Get(id)
		if err == nil {
			if err := c.backend.WalkLinks(ck.ID, CacheInfoLink{input, output, dgst, ""}, func(id string) error {
				refs[id] = struct{}{}
				return nil
			}); err != nil {
				return nil, err
			}
		}
	}

	if len(deps) == 0 {
		ck, err := c.getInternalKey(NewCacheKey(dgst, 0, nil), false)
		if err != nil {
			return nil, nil
		}
		refs[ck.CacheKeyInfo.ID] = struct{}{}
	}

	outs := make([]*CacheRecord, 0, len(refs))
	for id := range refs {
		cki, err := c.backend.Get(id)
		if err == nil {
			k := c.toInMemoryCacheKey(cki)
			if err := c.backend.WalkResults(id, func(r CacheResult) error {
				outs = append(outs, &CacheRecord{
					ID:           id + "@" + r.ID,
					CacheKey:     withExporter(k, &r),
					CacheManager: c,
					CreatedAt:    r.CreatedAt,
				})
				return nil
			}); err != nil {
				return nil, err
			}
		}
	}

	return outs, nil
}

func (c *inMemoryCacheManager) Load(ctx context.Context, rec *CacheRecord) (Result, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	keyParts := strings.Split(rec.ID, "@")
	if len(keyParts) != 2 {
		return nil, errors.Errorf("invalid cache record ID")
	}
	ck, err := c.getInternalKey(rec.CacheKey, false)
	if err != nil {
		return nil, err
	}

	res, err := c.backend.Load(ck.CacheKeyInfo.ID, keyParts[1])
	if err != nil {
		return nil, err
	}

	return c.results.Load(ctx, res)
}

func (c *inMemoryCacheManager) Save(k CacheKey, r Result) (ExportableCacheKey, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	empty := ExportableCacheKey{}

	ck, err := c.getInternalKey(k, true)
	if err != nil {
		return empty, err
	}

	res, err := c.results.Save(r)
	if err != nil {
		return empty, err
	}

	if err := c.backend.AddResult(ck.CacheKeyInfo.ID, res); err != nil {
		return empty, err
	}

	return withExporter(ck, &res), nil
}

func (c *inMemoryCacheManager) getInternalKey(k CacheKey, createIfNotExist bool) (*inMemoryCacheKey, error) {
	if ck, ok := k.(ExportableCacheKey); ok {
		k = ck.CacheKey
	}
	if ck, ok := k.(*inMemoryCacheKey); ok {
		return ck, nil
	}
	internalV := k.GetValue(internalMemoryKey)
	if internalV != nil {
		ck, err := c.backend.Get(internalV.(string))
		if err != nil {
			return nil, errors.Wrapf(err, "failed lookup by internal ID %s", internalV.(string))
		}
		return c.toInMemoryCacheKey(ck), nil
	}

	inputs := make([]CacheKeyInfoWithSelector, len(k.Deps()))
	dgstr := digest.SHA256.Digester()
	for i, inp := range k.Deps() {
		ck, err := c.getInternalKey(inp.CacheKey, createIfNotExist)
		if err != nil {
			return nil, err
		}
		inputs[i] = CacheKeyInfoWithSelector{ID: ck.CacheKeyInfo.ID, Selector: inp.Selector}
		if _, err := dgstr.Hash().Write([]byte(fmt.Sprintf("%s:%s,", ck.CacheKeyInfo.ID, inp.Selector))); err != nil {
			return nil, err
		}
	}

	if _, err := dgstr.Hash().Write([]byte(k.Digest())); err != nil {
		return nil, err
	}

	if _, err := dgstr.Hash().Write([]byte(fmt.Sprintf("%d", k.Output()))); err != nil {
		return nil, err
	}

	internalKey := string(dgstr.Digest())
	cki, err := c.backend.Get(internalKey)
	if err != nil {
		if errors.Cause(err) == ErrNotFound {
			if !createIfNotExist {
				return nil, err
			}
		} else {
			return nil, err
		}

		cki = CacheKeyInfo{
			ID:     internalKey,
			Base:   k.Digest(),
			Output: int(k.Output()),
			Deps:   inputs,
		}

		if err := c.backend.Set(cki.ID, cki); err != nil {
			return nil, err
		}

		for i, inp := range inputs {
			if cki.Base == "" {
				i = -1
			}

			err := c.backend.AddLink(inp.ID, CacheInfoLink{
				Input:    Index(i),
				Output:   Index(cki.Output),
				Digest:   cki.Base,
				Selector: inp.Selector,
			}, cki.ID)
			if err != nil {
				return nil, err
			}
		}
	}

	ck := &inMemoryCacheKey{
		CacheKey:     k,
		CacheKeyInfo: cki,
		manager:      c,
		deps:         k.Deps(),
	}
	ck.SetValue(internalMemoryKey, internalKey)

	return ck, nil
}

func newCombinedCacheManager(cms []CacheManager, main CacheManager) CacheManager {
	return &combinedCacheManager{cms: cms, main: main}
}

type combinedCacheManager struct {
	cms    []CacheManager
	main   CacheManager
	id     string
	idOnce sync.Once
}

func (cm *combinedCacheManager) ID() string {
	cm.idOnce.Do(func() {
		ids := make([]string, len(cm.cms))
		for i, c := range cm.cms {
			ids[i] = c.ID()
		}
		cm.id = digest.FromBytes([]byte(strings.Join(ids, ","))).String()
	})
	return cm.id
}

func (cm *combinedCacheManager) Query(inp []ExportableCacheKey, inputIndex Index, dgst digest.Digest, outputIndex Index, selector digest.Digest) ([]*CacheRecord, error) {
	eg, _ := errgroup.WithContext(context.TODO())
	res := make(map[string]*CacheRecord, len(cm.cms))
	var mu sync.Mutex
	for i, c := range cm.cms {
		func(i int, c CacheManager) {
			eg.Go(func() error {
				recs, err := c.Query(inp, inputIndex, dgst, outputIndex, selector)
				if err != nil {
					return err
				}
				mu.Lock()
				for _, r := range recs {
					if _, ok := res[r.ID]; !ok || c == cm.main {
						r.CacheManager = c
						if c == cm.main {
							r.Priority = 1
						}
						res[r.ID] = r
					}
				}
				mu.Unlock()
				return nil
			})
		}(i, c)
	}

	if err := eg.Wait(); err != nil {
		return nil, err
	}

	out := make([]*CacheRecord, 0, len(res))
	for _, r := range res {
		out = append(out, r)
	}
	return out, nil
}

func (cm *combinedCacheManager) Load(ctx context.Context, rec *CacheRecord) (Result, error) {
	return rec.CacheManager.Load(ctx, rec)
}

func (cm *combinedCacheManager) Save(key CacheKey, s Result) (ExportableCacheKey, error) {
	return cm.main.Save(key, s)
}
