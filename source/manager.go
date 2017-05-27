package source

import (
	"context"
	"sync"

	"github.com/pkg/errors"
	"github.com/tonistiigi/buildkit_poc/cache"
)

type Source interface {
	ID() string
	Pull(ctx context.Context, id Identifier) (cache.ImmutableRef, error)
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

func (sm *Manager) Pull(ctx context.Context, id Identifier) (cache.ImmutableRef, error) {
	sm.mu.Lock()
	src, ok := sm.sources[id.ID()]
	sm.mu.Unlock()

	if !ok {
		return nil, errors.Errorf("no handler fro %s", id.ID())
	}

	return src.Pull(ctx, id)
}
