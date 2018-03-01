package solver

import (
	"context"
	"sync"
	"sync/atomic"

	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

// SharedResult is a result that can be cloned
type SharedResult struct {
	mu   sync.Mutex
	main Result
}

func NewSharedResult(main Result) *SharedResult {
	return &SharedResult{main: main}
}

func (r *SharedResult) Clone() Result {
	r.mu.Lock()
	defer r.mu.Unlock()

	r1, r2 := dup(r.main)
	r.main = r1
	return r2
}

func (r *SharedResult) Release(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.main.Release(ctx)
}

func dup(res Result) (Result, Result) {
	sem := int64(0)
	return &splitResult{Result: res, sem: &sem}, &splitResult{Result: res, sem: &sem}
}

type splitResult struct {
	Result
	released int64
	sem      *int64
}

func (r *splitResult) Release(ctx context.Context) error {
	if atomic.AddInt64(&r.released, 1) > 1 {
		err := errors.Errorf("releasing already released reference")
		logrus.Error(err)
		return err
	}
	if atomic.AddInt64(r.sem, 1) == 2 {
		return r.Result.Release(ctx)
	}
	return nil
}

// NewCachedResult combines a result and cache key into cached result
func NewCachedResult(res Result, k CacheKey, exp Exporter) CachedResult {
	return &cachedResult{res, k, exp}
}

type cachedResult struct {
	Result
	k   CacheKey
	exp Exporter
}

func (cr *cachedResult) CacheKey() ExportableCacheKey {
	return ExportableCacheKey{CacheKey: cr.k, Exporter: cr.exp}
}

func (cr *cachedResult) Export(ctx context.Context, converter func(context.Context, Result) (*Remote, error)) ([]ExportRecord, error) {
	m := make(map[digest.Digest]*ExportRecord)
	if _, err := cr.exp.Export(ctx, m, converter); err != nil {
		return nil, err
	}
	out := make([]ExportRecord, 0, len(m))
	for _, r := range m {
		out = append(out, *r)
	}
	return out, nil
}

func NewSharedCachedResult(res CachedResult) *SharedCachedResult {
	return &SharedCachedResult{
		SharedResult: NewSharedResult(res),
		CachedResult: res,
	}
}

func (r *SharedCachedResult) Clone() CachedResult {
	return &clonedCachedResult{Result: r.SharedResult.Clone(), cr: r.CachedResult}
}

func (r *SharedCachedResult) Release(ctx context.Context) error {
	return r.SharedResult.Release(ctx)
}

type clonedCachedResult struct {
	Result
	cr CachedResult
}

func (r *clonedCachedResult) ID() string {
	return r.Result.ID()
}

func (cr *clonedCachedResult) CacheKey() ExportableCacheKey {
	return cr.cr.CacheKey()
}

func (cr *clonedCachedResult) Export(ctx context.Context, converter func(context.Context, Result) (*Remote, error)) ([]ExportRecord, error) {
	return cr.cr.Export(ctx, converter)
}

type SharedCachedResult struct {
	*SharedResult
	CachedResult
}
