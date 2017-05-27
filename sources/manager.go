package sources

import (
	"context"
	"sync"

	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/rootfs"
	"github.com/containerd/containerd/snapshot"
	"github.com/pkg/errors"
	"github.com/tonistiigi/buildkit_poc/cachemanager"
	"github.com/tonistiigi/buildkit_poc/sources/identifier"
)

type ContainerImageSourceOpt struct {
	Snapshotter   snapshot.Snapshotter
	ContentStore  content.Store
	Applier       rootfs.Applier
	CacheAccessor cachemanager.CacheAccessor
}

type Source interface {
	ID() string
	Pull(ctx context.Context, id identifier.Identifier) (cachemanager.SnapshotRef, error)
}

type SourceManager struct {
	mu      sync.Mutex
	sources map[string]Source
}

func NewSourceManager() (*SourceManager, error) {
	return &SourceManager{
		sources: make(map[string]Source),
	}, nil
}

func (sm *SourceManager) Register(src Source) {
	sm.mu.Lock()
	sm.sources[src.ID()] = src
	sm.mu.Unlock()
}

func (sm *SourceManager) Pull(ctx context.Context, id identifier.Identifier) (cachemanager.SnapshotRef, error) {
	sm.mu.Lock()
	src, ok := sm.sources[id.ID()]
	sm.mu.Unlock()

	if !ok {
		return nil, errors.Errorf("no handler fro %s", id.ID())
	}

	return src.Pull(ctx, id)
}
