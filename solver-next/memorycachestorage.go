package solver

import (
	"context"
	"sync"
	"time"

	"github.com/pkg/errors"
)

func NewInMemoryCacheStorage() CacheKeyStorage {
	return &inMemoryStore{
		byID:     map[string]*inMemoryKey{},
		byResult: map[string]map[string]struct{}{},
	}
}

type inMemoryStore struct {
	mu       sync.RWMutex
	byID     map[string]*inMemoryKey
	byResult map[string]map[string]struct{}
}

type inMemoryKey struct {
	id        string
	results   map[string]CacheResult
	links     map[CacheInfoLink]map[string]struct{}
	backlinks map[string]struct{}
}

func (s *inMemoryStore) Exists(id string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if k, ok := s.byID[id]; ok {
		return len(k.links) > 0
	}
	return false
}

func newKey(id string) *inMemoryKey {
	return &inMemoryKey{
		results:   map[string]CacheResult{},
		links:     map[CacheInfoLink]map[string]struct{}{},
		backlinks: map[string]struct{}{},
		id:        id,
	}
}

func (s *inMemoryStore) Walk(fn func(string) error) error {
	s.mu.RLock()
	ids := make([]string, 0, len(s.byID))
	for id := range s.byID {
		ids = append(ids, id)
	}
	s.mu.RUnlock()

	for _, id := range ids {
		if err := fn(id); err != nil {
			return err
		}
	}
	return nil
}

func (s *inMemoryStore) WalkResults(id string, fn func(CacheResult) error) error {
	s.mu.RLock()

	k, ok := s.byID[id]
	if !ok {
		s.mu.RUnlock()
		return nil
	}
	copy := make([]CacheResult, 0, len(k.results))
	for _, res := range k.results {
		copy = append(copy, res)
	}
	s.mu.RUnlock()

	for _, res := range copy {
		if err := fn(res); err != nil {
			return err
		}
	}
	return nil
}

func (s *inMemoryStore) Load(id string, resultID string) (CacheResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	k, ok := s.byID[id]
	if !ok {
		return CacheResult{}, errors.Wrapf(ErrNotFound, "no such key %s", id)
	}
	r, ok := k.results[resultID]
	if !ok {
		return CacheResult{}, errors.WithStack(ErrNotFound)
	}
	return r, nil
}

func (s *inMemoryStore) AddResult(id string, res CacheResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok := s.byID[id]
	if !ok {
		k = newKey(id)
		s.byID[id] = k
	}
	k.results[res.ID] = res
	m, ok := s.byResult[res.ID]
	if !ok {
		m = map[string]struct{}{}
		s.byResult[res.ID] = m
	}
	m[id] = struct{}{}
	return nil
}

func (s *inMemoryStore) Release(resultID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	ids, ok := s.byResult[resultID]
	if !ok {
		return nil
	}

	for id := range ids {
		k, ok := s.byID[id]
		if !ok {
			continue
		}

		delete(k.results, resultID)
		delete(s.byResult[resultID], id)
		if len(s.byResult[resultID]) == 0 {
			delete(s.byResult, resultID)
		}

		s.emptyBranchWithParents(k)
	}

	return nil
}

func (s *inMemoryStore) emptyBranchWithParents(k *inMemoryKey) {
	if len(k.results) != 0 || len(k.links) != 0 {
		return
	}
	for id := range k.backlinks {
		p, ok := s.byID[id]
		if !ok {
			continue
		}
		for l := range p.links {
			delete(p.links[l], k.id)
			if len(p.links[l]) == 0 {
				delete(p.links, l)
			}
		}
		s.emptyBranchWithParents(p)
	}

	delete(s.byID, k.id)
}

func (s *inMemoryStore) AddLink(id string, link CacheInfoLink, target string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok := s.byID[id]
	if !ok {
		k = newKey(id)
		s.byID[id] = k
	}
	k2, ok := s.byID[target]
	if !ok {
		k2 = newKey(target)
		s.byID[target] = k2
	}
	m, ok := k.links[link]
	if !ok {
		m = map[string]struct{}{}
		k.links[link] = m
	}

	k2.backlinks[id] = struct{}{}
	m[target] = struct{}{}
	return nil
}

func (s *inMemoryStore) WalkLinks(id string, link CacheInfoLink, fn func(id string) error) error {
	s.mu.RLock()
	k, ok := s.byID[id]
	if !ok {
		s.mu.RUnlock()
		return nil
	}
	var links []string
	for target := range k.links[link] {
		links = append(links, target)
	}
	s.mu.RUnlock()

	for _, t := range links {
		if err := fn(t); err != nil {
			return err
		}
	}
	return nil
}

func NewInMemoryResultStorage() CacheResultStorage {
	return &inMemoryResultStore{m: &sync.Map{}}
}

type inMemoryResultStore struct {
	m *sync.Map
}

func (s *inMemoryResultStore) Save(r Result) (CacheResult, error) {
	s.m.Store(r.ID(), r)
	return CacheResult{ID: r.ID(), CreatedAt: time.Now()}, nil
}

func (s *inMemoryResultStore) Load(ctx context.Context, res CacheResult) (Result, error) {
	v, ok := s.m.Load(res.ID)
	if !ok {
		return nil, errors.WithStack(ErrNotFound)
	}
	return v.(Result), nil
}

func (s *inMemoryResultStore) LoadRemote(ctx context.Context, res CacheResult) (*Remote, error) {
	return nil, nil
}

func (s *inMemoryResultStore) Exists(id string) bool {
	_, ok := s.m.Load(id)
	return ok
}
