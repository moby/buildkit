package solver

import (
	"sync"

	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/util/flightcontrol"
	"github.com/moby/buildkit/util/progress"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
	"golang.org/x/net/context"
)

// refCache holds the references to snapshots what are currently activve
// and allows sharing them between jobs

type refCache struct {
	mu    sync.Mutex
	cache map[digest.Digest]*cachedReq
	flightcontrol.Group
}

type cachedReq struct {
	jobs        map[*job]struct{}
	value       []*sharedRef
	progressCtx context.Context
}

func (c *refCache) probe(j *job, key digest.Digest) bool {
	c.mu.Lock()
	if c.cache == nil {
		c.cache = make(map[digest.Digest]*cachedReq)
	}
	cr, ok := c.cache[key]
	if !ok {
		cr = &cachedReq{jobs: make(map[*job]struct{})}
		c.cache[key] = cr
	}
	cr.jobs[j] = struct{}{}
	if ok && cr.value != nil {
		c.mu.Unlock()
		return true
	}
	c.mu.Unlock()
	return false
}
func (c *refCache) get(key digest.Digest) ([]cache.ImmutableRef, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.cache[key]
	// these errors should not be reached
	if !ok {
		return nil, errors.Errorf("no ref cache found")
	}
	if v.value == nil {
		return nil, errors.Errorf("no ref cache value set")
	}
	refs := make([]cache.ImmutableRef, 0, len(v.value))
	for _, r := range v.value {
		refs = append(refs, r.Clone())
	}
	return refs, nil
}
func (c *refCache) set(ctx context.Context, key digest.Digest, refs []cache.ImmutableRef) {
	c.mu.Lock()
	sharedRefs := make([]*sharedRef, 0, len(refs))
	for _, r := range refs {
		sharedRefs = append(sharedRefs, newSharedRef(r))
	}
	c.cache[key].value = sharedRefs
	c.cache[key].progressCtx = ctx
	c.mu.Unlock()
}
func (c *refCache) cancel(j *job) {
	c.mu.Lock()
	for k, r := range c.cache {
		if _, ok := r.jobs[j]; ok {
			delete(r.jobs, j)
		}
		if len(r.jobs) == 0 {
			for _, r := range r.value {
				go r.Release(context.TODO())
			}
			delete(c.cache, k)
		}
	}
	c.mu.Unlock()
}

func (c *refCache) writeProgressSnapshot(ctx context.Context, key digest.Digest) error {
	pw, ok, _ := progress.FromContext(ctx)
	if ok {
		c.mu.Lock()
		v, ok := c.cache[key]
		if !ok {
			c.mu.Unlock()
			return errors.Errorf("no ref cache found")
		}
		pctx := v.progressCtx
		c.mu.Unlock()
		if pctx != nil {
			return flightcontrol.WriteProgress(pctx, pw)
		}
	}
	return nil
}

// sharedRef is a wrapper around releasable that allows you to make new
// releasable child objects
type sharedRef struct {
	mu   sync.Mutex
	refs map[*sharedRefInstance]struct{}
	main cache.ImmutableRef
	cache.ImmutableRef
}

func newSharedRef(main cache.ImmutableRef) *sharedRef {
	mr := &sharedRef{
		refs:         make(map[*sharedRefInstance]struct{}),
		ImmutableRef: main,
	}
	mr.main = mr.Clone()
	return mr
}

func (mr *sharedRef) Clone() cache.ImmutableRef {
	mr.mu.Lock()
	r := &sharedRefInstance{sharedRef: mr}
	mr.refs[r] = struct{}{}
	mr.mu.Unlock()
	return r
}

func (mr *sharedRef) Release(ctx context.Context) error {
	return mr.main.Release(ctx)
}

type sharedRefInstance struct {
	*sharedRef
}

func (r *sharedRefInstance) Release(ctx context.Context) error {
	r.sharedRef.mu.Lock()
	defer r.sharedRef.mu.Unlock()
	delete(r.sharedRef.refs, r)
	if len(r.sharedRef.refs) == 0 {
		return r.sharedRef.ImmutableRef.Release(ctx)
	}
	return nil
}
