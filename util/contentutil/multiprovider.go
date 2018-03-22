package contentutil

import (
	"context"
	"sync"

	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/errdefs"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
)

func NewMultiProvider(base content.Provider) *MultiProvider {
	return &MultiProvider{
		base: base,
		sub:  map[digest.Digest]content.Provider{},
	}
}

type MultiProvider struct {
	mu   sync.RWMutex
	base content.Provider
	sub  map[digest.Digest]content.Provider
}

func (mp *MultiProvider) ReaderAt(ctx context.Context, dgst digest.Digest) (content.ReaderAt, error) {
	mp.mu.RLock()
	if p, ok := mp.sub[dgst]; ok {
		mp.mu.RUnlock()
		return p.ReaderAt(ctx, dgst)
	}
	mp.mu.RUnlock()
	if mp.base == nil {
		return nil, errors.Wrapf(errdefs.ErrNotFound, "content %v", dgst)
	}
	return mp.base.ReaderAt(ctx, dgst)
}

func (mp *MultiProvider) Add(dgst digest.Digest, p content.Provider) {
	mp.mu.Lock()
	defer mp.mu.Unlock()
	mp.sub[dgst] = p
}
