package solver

import (
	"context"
	"sync"
	"time"

	"github.com/pkg/errors"
)

func NewInMemoryCacheStorage() CacheKeyStorage {
	return &inMemoryStore{byID: map[string]*inMemoryKey{}}
}

type inMemoryStore struct {
	mu   sync.RWMutex
	byID map[string]*inMemoryKey
}

type inMemoryKey struct {
	CacheKeyInfo

	results map[string]CacheResult
	links   map[CacheInfoLink]map[string]struct{}
}

func (s *inMemoryStore) Get(id string) (CacheKeyInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	k, ok := s.byID[id]
	if !ok {
		return CacheKeyInfo{}, errors.WithStack(ErrNotFound)
	}
	return k.CacheKeyInfo, nil
}

func (s *inMemoryStore) Set(id string, info CacheKeyInfo) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok := s.byID[id]
	if !ok {
		k = &inMemoryKey{
			results: map[string]CacheResult{},
			links:   map[CacheInfoLink]map[string]struct{}{},
		}
		s.byID[id] = k
	}
	k.CacheKeyInfo = info
	return nil
}

func (s *inMemoryStore) WalkResults(id string, fn func(CacheResult) error) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	k, ok := s.byID[id]
	if !ok {
		return nil
	}
	for _, res := range k.results {
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
		return errors.Wrapf(ErrNotFound, "no such key %s", id)
	}
	k.results[res.ID] = res
	return nil
}

func (s *inMemoryStore) Release(resultID string) error {
	return errors.Errorf("not-implemented")
}

func (s *inMemoryStore) AddLink(id string, link CacheInfoLink, target string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok := s.byID[id]
	if !ok {
		return errors.Wrapf(ErrNotFound, "no such key %s", id)
	}
	m, ok := k.links[link]
	if !ok {
		m = map[string]struct{}{}
		k.links[link] = m
	}

	m[target] = struct{}{}
	return nil
}

func (s *inMemoryStore) WalkLinks(id string, link CacheInfoLink, fn func(id string) error) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	k, ok := s.byID[id]
	if !ok {
		return errors.Wrapf(ErrNotFound, "no such key %s", id)
	}
	for target := range k.links[link] {
		if err := fn(target); err != nil {
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
