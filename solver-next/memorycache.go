package solver

import (
	"context"
	"fmt"
	"strings"
	"sync"

	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
)

type internalMemoryKeyT string

var internalMemoryKey = internalMemoryKeyT("buildkit/memory-cache-id")

func NewInMemoryCacheManager() CacheManager {
	return &inMemoryCacheManager{
		byID: map[string]*inMemoryCacheKey{},
	}
}

type inMemoryCacheKey struct {
	CacheKey
	id     string
	dgst   digest.Digest
	output Index
	deps   []CacheKey // only []*inMemoryCacheManager

	results map[Index]map[string]Result
	links   map[link]map[string]struct{}
}

func (ck *inMemoryCacheKey) Deps() []CacheKey {
	return ck.deps
}
func (ck *inMemoryCacheKey) Digest() digest.Digest {
	return ck.dgst
}
func (ck *inMemoryCacheKey) Index() Index {
	return ck.output
}

type link struct {
	input, output Index
	digest        digest.Digest
}

type inMemoryCacheManager struct {
	mu   sync.RWMutex
	byID map[string]*inMemoryCacheKey
}

func (c *inMemoryCacheManager) Query(deps []CacheKey, input Index, dgst digest.Digest, output Index) ([]*CacheRecord, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	refs := map[string]struct{}{}
	sublinks := map[string]struct{}{}

	for _, dep := range deps {
		ck, err := c.getInternalKey(dep, false)
		if err == nil {
			for key := range ck.links[link{input, output, dgst}] {
				refs[key] = struct{}{}
			}
			for key := range ck.links[link{Index(-1), Index(0), ""}] {
				sublinks[key] = struct{}{}
			}
		}
	}

	for id := range sublinks {
		if ck, ok := c.byID[id]; ok {
			for key := range ck.links[link{input, output, dgst}] {
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

	outs := make([]*CacheRecord, 0, len(refs))
	for id := range refs {
		if ck, ok := c.byID[id]; ok {
			for _, res := range ck.results[output] {
				outs = append(outs, &CacheRecord{
					ID:       id + "@" + res.ID(),
					CacheKey: ck,
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
			return res, nil
		}
	}
	return nil, errors.Errorf("failed to load cache record") // TODO: typed error
}

func (c *inMemoryCacheManager) Save(k CacheKey, r Result) (CacheKey, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	ck, err := c.getInternalKey(k, true)
	if err != nil {
		return nil, err
	}
	if err := c.addResult(ck, k.Output(), r); err != nil {
		return nil, err
	}
	return ck, nil
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
	inputs := make([]CacheKey, len(k.Deps()))
	dgstr := digest.SHA256.Digester()
	for i, inp := range k.Deps() {
		ck, err := c.getInternalKey(inp, createIfNotExist)
		if err != nil {
			return nil, err
		}
		inputs[i] = ck
		if _, err := dgstr.Hash().Write([]byte(ck.id)); err != nil {
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
			results:  map[Index]map[string]Result{},
			links:    map[link]map[string]struct{}{},
		}
		ck.SetValue(internalMemoryKey, internalKey)
		c.byID[internalKey] = ck
	}

	for i, inp := range inputs {
		if ck.dgst == "" {
			i = -1
		}
		if err := c.addLink(link{Index(i), ck.output, ck.dgst}, inp.(*inMemoryCacheKey), ck); err != nil {
			return nil, err
		}
	}

	return ck, nil
}

func (c *inMemoryCacheManager) addResult(ck *inMemoryCacheKey, output Index, r Result) error {
	m, ok := ck.results[output]
	if !ok {
		m = map[string]Result{}
		ck.results[output] = m
	}
	m[r.ID()] = r
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
