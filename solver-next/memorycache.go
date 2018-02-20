package solver

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/moby/buildkit/identity"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
)

type internalMemoryKeyT string

var internalMemoryKey = internalMemoryKeyT("buildkit/memory-cache-id")

var NoSelector = digest.FromBytes(nil)

func NewInMemoryCacheManager() CacheManager {
	return &inMemoryCacheManager{
		byID: map[string]*inMemoryCacheKey{},
		id:   identity.NewID(),
	}
}

type inMemoryCacheKey struct {
	CacheKey
	id     string
	dgst   digest.Digest
	output Index
	deps   []CacheKeyWithSelector // only []*inMemoryCacheKey

	results map[Index]map[string]savedResult
	links   map[link]map[string]struct{}
}

func (ck *inMemoryCacheKey) Deps() []CacheKeyWithSelector {
	return ck.deps
}
func (ck *inMemoryCacheKey) Digest() digest.Digest {
	return ck.dgst
}
func (ck *inMemoryCacheKey) Index() Index {
	return ck.output
}

func withExporter(ck *inMemoryCacheKey, result Result, deps [][]CacheKeyWithSelector) ExportableCacheKey {
	return ExportableCacheKey{ck, &cacheExporter{
		inMemoryCacheKey: ck,
		result:           result,
		deps:             deps,
	}}
}

type cacheExporter struct {
	*inMemoryCacheKey
	result Result
	deps   [][]CacheKeyWithSelector
}

func (ce *cacheExporter) Export(ctx context.Context, m map[digest.Digest]*ExportRecord, converter func(context.Context, Result) (*Remote, error)) (*ExportRecord, error) {
	remote, err := converter(ctx, ce.result)
	if err != nil {
		return nil, err
	}

	cacheID := digest.FromBytes([]byte(ce.inMemoryCacheKey.id))
	if len(remote.Descriptors) > 0 && remote.Descriptors[0].Digest != "" {
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

	for i, deps := range ce.deps {
		for _, dep := range deps {
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
	}

	return rec, nil
}

type savedResult struct {
	result    Result
	createdAt time.Time
}

type link struct {
	input, output Index
	dgst          digest.Digest
	selector      digest.Digest
}

type inMemoryCacheManager struct {
	mu   sync.RWMutex
	byID map[string]*inMemoryCacheKey
	id   string
}

func (c *inMemoryCacheManager) ID() string {
	return c.id
}

func (c *inMemoryCacheManager) Query(deps []ExportableCacheKey, input Index, dgst digest.Digest, output Index, selector digest.Digest) ([]*CacheRecord, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	refs := map[string]struct{}{}
	sublinks := map[string]struct{}{}

	allDeps := make([][]CacheKeyWithSelector, int(input)+1)

	for _, dep := range deps {
		ck, err := c.getInternalKey(dep, false)
		if err == nil {
			for key := range ck.links[link{input, output, dgst, selector}] {
				refs[key] = struct{}{}
			}
			for key := range ck.links[link{Index(-1), Index(0), "", selector}] {
				sublinks[key] = struct{}{}
			}
			for key := range ck.links[link{Index(-1), Index(0), "", NoSelector}] {
				sublinks[key] = struct{}{}
			}
		}
	}

	for id := range sublinks {
		if ck, ok := c.byID[id]; ok {
			for key := range ck.links[link{input, output, dgst, ""}] {
				refs[key] = struct{}{}
			}
		}
	}

	if len(deps) == 0 {
		ck, err := c.getInternalKey(NewCacheKey(dgst, 0, nil), false)
		if err != nil {
			return nil, nil
		}
		refs[ck.id] = struct{}{}
	}

	for _, d := range deps {
		allDeps[int(input)] = append(allDeps[int(input)], CacheKeyWithSelector{CacheKey: d, Selector: selector})
	}

	outs := make([]*CacheRecord, 0, len(refs))
	for id := range refs {
		if ck, ok := c.byID[id]; ok {
			for _, res := range ck.results[output] {
				outs = append(outs, &CacheRecord{
					ID:           id + "@" + res.result.ID(),
					CacheKey:     withExporter(ck, res.result, allDeps),
					CacheManager: c,
					CreatedAt:    res.createdAt,
				})
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

	for output := range ck.results {
		res, ok := ck.results[output][keyParts[1]]
		if ok {
			return res.result, nil
		}
	}
	return nil, errors.Errorf("failed to load cache record") // TODO: typed error
}

func (c *inMemoryCacheManager) Save(k CacheKey, r Result) (ExportableCacheKey, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	empty := ExportableCacheKey{}

	ck, err := c.getInternalKey(k, true)
	if err != nil {
		return empty, err
	}
	if err := c.addResult(ck, k.Output(), r); err != nil {
		return empty, err
	}
	allDeps := make([][]CacheKeyWithSelector, len(k.Deps()))
	for i, d := range k.Deps() {
		allDeps[i] = append(allDeps[i], d)
	}

	return withExporter(ck, r, allDeps), nil
}

func (c *inMemoryCacheManager) getInternalKey(k CacheKey, createIfNotExist bool) (*inMemoryCacheKey, error) {
	if ck, ok := k.(*inMemoryCacheKey); ok {
		return ck, nil
	}
	internalV := k.GetValue(internalMemoryKey)
	if internalV != nil {
		ck, ok := c.byID[internalV.(string)]
		if !ok {
			return nil, errors.Errorf("failed lookup by internal ID %s", internalV.(string))
		}
		return ck, nil
	}
	inputs := make([]CacheKeyWithSelector, len(k.Deps()))
	dgstr := digest.SHA256.Digester()
	for i, inp := range k.Deps() {
		ck, err := c.getInternalKey(inp.CacheKey, createIfNotExist)
		if err != nil {
			return nil, err
		}
		inputs[i] = CacheKeyWithSelector{CacheKey: ExportableCacheKey{CacheKey: ck}, Selector: inp.Selector}
		if _, err := dgstr.Hash().Write([]byte(fmt.Sprintf("%s:%s,", ck.id, inp.Selector))); err != nil {
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
	ck, ok := c.byID[internalKey]
	if !ok {
		if !createIfNotExist {
			return nil, errors.Errorf("not-found")
		}
		ck = &inMemoryCacheKey{
			CacheKey: k,
			id:       internalKey,
			dgst:     k.Digest(),
			output:   k.Output(),
			deps:     inputs,
			results:  map[Index]map[string]savedResult{},
			links:    map[link]map[string]struct{}{},
		}
		ck.SetValue(internalMemoryKey, internalKey)
		c.byID[internalKey] = ck
	}

	for i, inp := range inputs {
		if ck.dgst == "" {
			i = -1
		}
		if err := c.addLink(link{Index(i), ck.output, ck.dgst, inp.Selector}, inp.CacheKey.CacheKey.(*inMemoryCacheKey), ck); err != nil {
			return nil, err
		}
	}

	return ck, nil
}

func (c *inMemoryCacheManager) addResult(ck *inMemoryCacheKey, output Index, r Result) error {
	m, ok := ck.results[output]
	if !ok {
		m = map[string]savedResult{}
		ck.results[output] = m
	}
	m[r.ID()] = savedResult{result: r, createdAt: time.Now()}
	return nil
}

func (c *inMemoryCacheManager) addLink(l link, from, to *inMemoryCacheKey) error {
	m, ok := from.links[l]
	if !ok {
		m = map[string]struct{}{}
		from.links[l] = m
	}
	m[to.id] = struct{}{}
	return nil
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
