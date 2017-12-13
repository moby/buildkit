package solver

import (
	"sync"

	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/solver/reference"
	"golang.org/x/net/context"
)

// sharedRef is a wrapper around releasable that allows you to make new
// releasable child objects
type sharedRef struct {
	mu   sync.Mutex
	refs map[*sharedRefInstance]struct{}
	main reference.Ref
	reference.Ref
}

func newSharedRef(main reference.Ref) *sharedRef {
	mr := &sharedRef{
		refs: make(map[*sharedRefInstance]struct{}),
		Ref:  main,
	}
	mr.main = mr.Clone()
	return mr
}

func (mr *sharedRef) Clone() reference.Ref {
	mr.mu.Lock()
	r := &sharedRefInstance{sharedRef: mr}
	mr.refs[r] = struct{}{}
	mr.mu.Unlock()
	return r
}

func (mr *sharedRef) Release(ctx context.Context) error {
	return mr.main.Release(ctx)
}

func (mr *sharedRef) Sys() reference.Ref {
	sys := mr.Ref
	if s, ok := sys.(interface {
		Sys() reference.Ref
	}); ok {
		return s.Sys()
	}
	return sys
}

type sharedRefInstance struct {
	*sharedRef
}

func (r *sharedRefInstance) Release(ctx context.Context) error {
	r.sharedRef.mu.Lock()
	defer r.sharedRef.mu.Unlock()
	delete(r.sharedRef.refs, r)
	if len(r.sharedRef.refs) == 0 {
		return r.sharedRef.Ref.Release(ctx)
	}
	return nil
}

func originRef(ref reference.Ref) reference.Ref {
	sysRef := ref
	if sys, ok := ref.(interface {
		Sys() reference.Ref
	}); ok {
		sysRef = sys.Sys()
	}
	return sysRef
}

func toImmutableRef(ref reference.Ref) (cache.ImmutableRef, bool) {
	immutable, ok := originRef(ref).(cache.ImmutableRef)
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
