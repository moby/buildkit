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
	"golang.org/x/sync/errgroup"
)

type internalMemoryKeyT string

var internalMemoryKey = internalMemoryKeyT("buildkit/memory-cache-id")

var NoSelector = digest.FromBytes(nil)

func NewInMemoryCacheManager() CacheManager {
	return NewCacheManager(identity.NewID(), NewInMemoryCacheStorage(), NewInMemoryResultStorage())
}

func NewCacheManager(id string, storage CacheKeyStorage, results CacheResultStorage) CacheManager {
	cm := &inMemoryCacheManager{
		id:      id,
		backend: storage,
		results: results,
	}

	storage.Walk(func(id string) error {
		return storage.WalkResults(id, func(cr CacheResult) error {
			if !results.Exists(cr.ID) {
				storage.Release(cr.ID)
			}
			return nil
		})
	})

	return cm
}

type inMemoryCacheKey struct {
	manager     *inMemoryCacheManager
	cacheResult CacheResult
	deps        []CacheKeyWithSelector // only []*inMemoryCacheKey
	digest      digest.Digest
	output      Index
	id          string
	CacheKey
}

func (ck *inMemoryCacheKey) Deps() []CacheKeyWithSelector {
	return ck.deps
}

func (ck *inMemoryCacheKey) Digest() digest.Digest {
	return ck.digest
}
func (ck *inMemoryCacheKey) Output() Index {
	return ck.output
}

func withExporter(ck *inMemoryCacheKey, cacheResult *CacheResult, deps []CacheKeyWithSelector) ExportableCacheKey {
	return ExportableCacheKey{ck, &cacheExporter{
		inMemoryCacheKey: ck,
		cacheResult:      cacheResult,
		deps:             deps,
	}}
}

type cacheExporter struct {
	*inMemoryCacheKey
	cacheResult *CacheResult
	deps        []CacheKeyWithSelector
}

func (ce *cacheExporter) Export(ctx context.Context, m map[digest.Digest]*ExportRecord, converter func(context.Context, Result) (*Remote, error)) (*ExportRecord, error) {
	var res Result
	if ce.cacheResult == nil {
		cr, err := ce.inMemoryCacheKey.manager.getBestResult(ce.inMemoryCacheKey.id)
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

	cacheID := digest.FromBytes([]byte(ce.inMemoryCacheKey.id))
	if remote != nil && len(remote.Descriptors) > 0 && remote.Descriptors[0].Digest != "" {
		cacheID = remote.Descriptors[0].Digest
	}

	rec, ok := m[cacheID]
	if !ok {
		rec = &ExportRecord{
			Digest: cacheID,
			Remote: remote,
			Links:  make(map[CacheLink]struct{}),
		}
		m[cacheID] = rec
	}

	if len(ce.Deps()) == 0 {
		rec.Links[CacheLink{
			Output: ce.Output(),
			Base:   ce.Digest(),
		}] = struct{}{}
	}

	for i, dep := range ce.deps {
		if dep.CacheKey.Exporter == nil {
			continue
		}
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

func (c *inMemoryCacheManager) toInMemoryCacheKey(id string, dgst digest.Digest, output Index, deps []CacheKeyWithSelector) *inMemoryCacheKey {
	ck := &inMemoryCacheKey{
		id:       id,
		output:   output,
		digest:   dgst,
		manager:  c,
		CacheKey: NewCacheKey("", 0, nil),
		deps:     deps,
	}
	ck.SetValue(internalMemoryKey, id)
	return ck
}

func (c *inMemoryCacheManager) getBestResult(id string) (*CacheResult, error) {
	var results []*CacheResult
	if err := c.backend.WalkResults(id, func(res CacheResult) error {
		results = append(results, &res)
		return nil
	}); err != nil {
		return nil, err
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].CreatedAt.After(results[j].CreatedAt)
	})

	if len(results) > 0 {
		return results[0], nil
	}

	return nil, nil
}

func (c *inMemoryCacheManager) Query(deps []CacheKeyWithSelector, input Index, dgst digest.Digest, output Index) ([]*CacheRecord, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	type dep struct {
		results     map[string]struct{}
		key         CacheKeyWithSelector
		internalKey *inMemoryCacheKey
	}

	formatDeps := func(deps []dep, index int) []CacheKeyWithSelector {
		keys := make([]CacheKeyWithSelector, index+1)
		if len(deps) == 1 {
			keys[index] = deps[0].key
		} else {
			k2 := make([]CacheKeyWithSelector, 0, len(deps))
			for _, d := range deps {
				k2 = append(k2, d.key)
			}
			keys[index] = CacheKeyWithSelector{CacheKey: ExportableCacheKey{CacheKey: NewCacheKey("", 0, k2)}}
		}
		return keys
	}

	allDeps := make([]dep, 0, len(deps))

	for _, d := range deps {
		for _, k := range c.getAllKeys(d) {
			d := dep{key: k, results: map[string]struct{}{}}
			internalKey, err := c.getInternalKey(k.CacheKey, false)
			if err != nil {
				if errors.Cause(err) == ErrNotFound {
					allDeps = append(allDeps, d)
				} else {
					return nil, err
				}
			} else {
				d.internalKey = internalKey
			}
			allDeps = append(allDeps, d)
		}
	}

	allRes := map[string]struct{}{}
	for _, d := range allDeps {
		if d.internalKey != nil {
			if err := c.backend.WalkLinks(d.internalKey.id, CacheInfoLink{input, output, dgst, d.key.Selector}, func(id string) error {
				d.results[id] = struct{}{}
				allRes[id] = struct{}{}
				return nil
			}); err != nil {
				return nil, err
			}
		}
	}

	if len(deps) == 0 {
		allRes[digest.FromBytes([]byte(fmt.Sprintf("%s@%d", dgst, output))).String()] = struct{}{}
	}

	outs := make([]*CacheRecord, 0, len(allRes))

	for res := range allRes {
		for _, d := range allDeps {
			if d.internalKey == nil {
				internalKey, err := c.getInternalKey(d.key.CacheKey, true)
				if err != nil {
					return nil, err
				}
				d.internalKey = internalKey
			}
			if _, ok := d.results[res]; !ok {
				if err := c.backend.AddLink(d.internalKey.id, CacheInfoLink{
					Input:    input,
					Output:   output,
					Digest:   dgst,
					Selector: d.key.Selector,
				}, res); err != nil {
					return nil, err
				}
			}
		}
		hadResults := false

		fdeps := formatDeps(allDeps, int(input))
		k := c.toInMemoryCacheKey(res, dgst, output, fdeps)
		// TODO: invoke this only once per input
		if err := c.backend.WalkResults(res, func(r CacheResult) error {
			if c.results.Exists(r.ID) {
				outs = append(outs, &CacheRecord{
					ID:           res + "@" + r.ID,
					CacheKey:     withExporter(k, &r, fdeps),
					CacheManager: c,
					Loadable:     true,
					CreatedAt:    r.CreatedAt,
				})
				hadResults = true
			} else {
				c.backend.Release(r.ID)
			}
			return nil
		}); err != nil {
			return nil, err
		}

		if !hadResults {
			if len(deps) == 0 {
				if !c.backend.Exists(res) {
					continue
				}
			}
			outs = append(outs, &CacheRecord{
				ID:           res,
				CacheKey:     withExporter(k, nil, fdeps),
				CacheManager: c,
				Loadable:     false,
			})
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

	res, err := c.backend.Load(ck.id, keyParts[1])
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

	if err := c.backend.AddResult(ck.id, res); err != nil {
		return empty, err
	}

	return withExporter(ck, &res, ck.Deps()), nil
}

func (c *inMemoryCacheManager) getInternalKeys(d CacheKeyWithSelector, createIfNotExist bool) ([]CacheKeyWithSelector, error) {
	keys := make([]CacheKeyWithSelector, 0, 1)
	if d.CacheKey.Digest() == "" {
		for _, d := range d.CacheKey.Deps() {
			k, err := c.getInternalKey(d.CacheKey, createIfNotExist)
			if err != nil {
				if !createIfNotExist && errors.Cause(err) == ErrNotFound {
					continue
				}
				return nil, err
			}
			keys = append(keys, CacheKeyWithSelector{Selector: d.Selector, CacheKey: ExportableCacheKey{CacheKey: k, Exporter: d.CacheKey.Exporter}})
		}
	} else {
		k, err := c.getInternalKey(d.CacheKey, createIfNotExist)
		if err != nil {
			return nil, err
		}
		keys = append(keys, CacheKeyWithSelector{Selector: d.Selector, CacheKey: ExportableCacheKey{CacheKey: k, Exporter: d.CacheKey.Exporter}})
	}
	return keys, nil
}

func (c *inMemoryCacheManager) getAllKeys(d CacheKeyWithSelector) []CacheKeyWithSelector {
	keys := make([]CacheKeyWithSelector, 0, 1)
	if d.CacheKey.Digest() == "" {
		for _, d := range d.CacheKey.Deps() {
			keys = append(keys, d)
		}
	} else {
		keys = append(keys, d)
	}
	return keys
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
		return c.toInMemoryCacheKey(internalV.(string), k.Digest(), k.Output(), k.Deps()), nil
	}

	matches := make(map[string]struct{})
	deps := make([][]CacheKeyWithSelector, 0, len(k.Deps()))
	for i, inp := range k.Deps() {
		allKeys := c.getAllKeys(inp)
		cks := make([]CacheKeyWithSelector, 0, len(allKeys))
		for _, k := range allKeys {
			internalKey, err := c.getInternalKey(k.CacheKey, createIfNotExist)
			if err == nil {
				cks = append(cks, CacheKeyWithSelector{Selector: k.Selector, CacheKey: ExportableCacheKey{CacheKey: internalKey, Exporter: k.CacheKey.Exporter}})
			}
		}

		if len(cks) == 0 {
			return nil, errors.WithStack(ErrNotFound)
		}

		if i == 0 || len(matches) > 0 {
			for _, ck := range cks {
				internalCk := ck.CacheKey.CacheKey.(*inMemoryCacheKey)
				m2 := make(map[string]struct{})
				if err := c.backend.WalkLinks(internalCk.id, CacheInfoLink{
					Input:    Index(i),
					Output:   Index(k.Output()),
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
					return nil, err
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
		deps = append(deps, cks)
	}

	var internalKey string
	if len(matches) == 0 && len(k.Deps()) > 0 {
		if createIfNotExist {
			internalKey = identity.NewID()
		} else {
			return nil, errors.WithStack(ErrNotFound)
		}
	} else {
		for k := range matches {
			internalKey = k
			break
		}
		if len(k.Deps()) == 0 {
			internalKey = digest.FromBytes([]byte(fmt.Sprintf("%s@%d", k.Digest(), k.Output()))).String()
		}
		return c.toInMemoryCacheKey(internalKey, k.Digest(), k.Output(), k.Deps()), nil
	}

	for i, dep := range deps {
		for _, ck := range dep {
			internalCk := ck.CacheKey.CacheKey.(*inMemoryCacheKey)
			err := c.backend.AddLink(internalCk.id, CacheInfoLink{
				Input:    Index(i),
				Output:   k.Output(),
				Digest:   k.Digest(),
				Selector: ck.Selector,
			}, internalKey)
			if err != nil {
				return nil, err
			}
		}
	}

	return c.toInMemoryCacheKey(internalKey, k.Digest(), k.Output(), k.Deps()), nil
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

func (cm *combinedCacheManager) Query(inp []CacheKeyWithSelector, inputIndex Index, dgst digest.Digest, outputIndex Index) ([]*CacheRecord, error) {
	eg, _ := errgroup.WithContext(context.TODO())
	res := make(map[string]*CacheRecord, len(cm.cms))
	var mu sync.Mutex
	for i, c := range cm.cms {
		func(i int, c CacheManager) {
			eg.Go(func() error {
				recs, err := c.Query(inp, inputIndex, dgst, outputIndex)
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
