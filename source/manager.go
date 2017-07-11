package source

import (
	"sync"

	"github.com/moby/buildkit/cache"
	"github.com/pkg/errors"
	"golang.org/x/net/context"
)

type Source interface {
	ID() string
	Resolve(ctx context.Context, id Identifier) (SourceInstance, error)
}

type SourceInstance interface {
	CacheKey(ctx context.Context) (string, error)
	Snapshot(ctx context.Context) (cache.ImmutableRef, error)
}

type Manager struct {
	mu      sync.Mutex
	sources map[string]Source
}

func NewManager() (*Manager, error) {
	return &Manager{
		sources: make(map[string]Source),
	}, nil
}

func (sm *Manager) Register(src Source) {
	sm.mu.Lock()
	sm.sources[src.ID()] = src
	sm.mu.Unlock()
}

func (sm *Manager) Resolve(ctx context.Context, id Identifier) (SourceInstance, error) {
	sm.mu.Lock()
	src, ok := sm.sources[id.ID()]
	sm.mu.Unlock()

	if !ok {
		return nil, errors.Errorf("no handler fro %s", id.ID())
	}

	return src.Resolve(ctx, id)
}
