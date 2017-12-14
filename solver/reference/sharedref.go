package reference

import (
	"sync"

	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/solver"
	"golang.org/x/net/context"
)

// SharedRef is a wrapper around releasable that allows you to make new
// releasable child objects
type SharedRef struct {
	mu   sync.Mutex
	refs map[*sharedRefInstance]struct{}
	main solver.Ref
	solver.Ref
}

func NewSharedRef(main solver.Ref) *SharedRef {
	mr := &SharedRef{
		refs: make(map[*sharedRefInstance]struct{}),
		Ref:  main,
	}
	mr.main = mr.Clone()
	return mr
}

func (mr *SharedRef) Clone() solver.Ref {
	mr.mu.Lock()
	r := &sharedRefInstance{SharedRef: mr}
	mr.refs[r] = struct{}{}
	mr.mu.Unlock()
	return r
}

func (mr *SharedRef) Release(ctx context.Context) error {
	return mr.main.Release(ctx)
}

func (mr *SharedRef) Sys() solver.Ref {
	sys := mr.Ref
	if s, ok := sys.(interface {
		Sys() solver.Ref
	}); ok {
		return s.Sys()
	}
	return sys
}

type sharedRefInstance struct {
	*SharedRef
}

func (r *sharedRefInstance) Release(ctx context.Context) error {
	r.SharedRef.mu.Lock()
	defer r.SharedRef.mu.Unlock()
	delete(r.SharedRef.refs, r)
	if len(r.SharedRef.refs) == 0 {
		return r.SharedRef.Ref.Release(ctx)
	}
	return nil
}

func OriginRef(ref solver.Ref) solver.Ref {
	sysRef := ref
	if sys, ok := ref.(interface {
		Sys() solver.Ref
	}); ok {
		sysRef = sys.Sys()
	}
	return sysRef
}

func ToImmutableRef(ref solver.Ref) (cache.ImmutableRef, bool) {
	immutable, ok := OriginRef(ref).(cache.ImmutableRef)
	if !ok {
		return nil, false
	}
	return &immutableRef{immutable, ref.Release}, true
}

type immutableRef struct {
	cache.ImmutableRef
	release func(context.Context) error
}

func (ir *immutableRef) Release(ctx context.Context) error {
	return ir.release(ctx)
}
