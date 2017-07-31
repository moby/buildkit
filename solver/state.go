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

// activeState holds the references to snapshots what are currently activve
// and allows sharing them between jobs

type activeState struct {
	mu     sync.Mutex
	states map[digest.Digest]*state
	flightcontrol.Group
}

type state struct {
	*activeState
	key         digest.Digest
	jobs        map[*job]struct{}
	refs        []*sharedRef
	cacheKey    digest.Digest
	op          Op
	progressCtx context.Context
	cacheCtx    context.Context
}

func (s *activeState) vertexState(ctx context.Context, key digest.Digest, cb func() (Op, error)) (*state, error) {
	jv := ctx.Value(jobKey)
	if jv == nil {
		return nil, errors.Errorf("can't get vertex state without active job")
	}
	j := jv.(*job)

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.states == nil {
		s.states = map[digest.Digest]*state{}
	}

	st, ok := s.states[key]
	if !ok {
		op, err := cb()
		if err != nil {
			return nil, err
		}
		st = &state{key: key, jobs: map[*job]struct{}{}, op: op, activeState: s}
		s.states[key] = st
	}
	st.jobs[j] = struct{}{}
	return st, nil
}

func (s *activeState) cancel(j *job) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, st := range s.states {
		if _, ok := st.jobs[j]; ok {
			delete(st.jobs, j)
		}
		if len(st.jobs) == 0 {
			for _, r := range st.refs {
				go r.Release(context.TODO())
			}
			delete(s.states, k)
		}
	}
}

func (s *state) GetRefs(ctx context.Context, index Index, cb func(context.Context, Op) ([]Reference, error)) (Reference, error) {
	_, err := s.Do(ctx, s.key.String(), func(doctx context.Context) (interface{}, error) {
		if s.refs != nil {
			if err := writeProgressSnapshot(s.progressCtx, ctx); err != nil {
				return nil, err
			}
			return nil, nil
		}
		refs, err := cb(doctx, s.op)
		if err != nil {
			return nil, err
		}
		sharedRefs := make([]*sharedRef, 0, len(refs))
		for _, r := range refs {
			sharedRefs = append(sharedRefs, newSharedRef(r))
		}
		s.refs = sharedRefs
		s.progressCtx = doctx
		return nil, nil
	})
	if err != nil {
		return nil, err
	}
	return s.refs[int(index)].Clone(), nil
}

func (s *state) GetCacheKey(ctx context.Context, cb func(context.Context, Op) (digest.Digest, error)) (digest.Digest, error) {
	_, err := s.Do(ctx, "cache:"+s.key.String(), func(doctx context.Context) (interface{}, error) {
		if s.cacheKey != "" {
			if err := writeProgressSnapshot(s.cacheCtx, ctx); err != nil {
				return nil, err
			}
			return nil, nil
		}
		cacheKey, err := cb(doctx, s.op)
		if err != nil {
			return nil, err
		}
		s.cacheKey = cacheKey
		s.cacheCtx = doctx
		return nil, nil
	})
	if err != nil {
		return "", err
	}
	return s.cacheKey, nil
}

func writeProgressSnapshot(srcCtx, destCtx context.Context) error {
	pw, ok, _ := progress.FromContext(destCtx)
	if ok {
		if srcCtx != nil {
			return flightcontrol.WriteProgress(srcCtx, pw)
		}
	}
	return nil
}

// sharedRef is a wrapper around releasable that allows you to make new
// releasable child objects
type sharedRef struct {
	mu   sync.Mutex
	refs map[*sharedRefInstance]struct{}
	main Reference
	Reference
}

func newSharedRef(main Reference) *sharedRef {
	mr := &sharedRef{
		refs:      make(map[*sharedRefInstance]struct{}),
		Reference: main,
	}
	mr.main = mr.Clone()
	return mr
}

func (mr *sharedRef) Clone() Reference {
	mr.mu.Lock()
	r := &sharedRefInstance{sharedRef: mr}
	mr.refs[r] = struct{}{}
	mr.mu.Unlock()
	return r
}

func (mr *sharedRef) Release(ctx context.Context) error {
	return mr.main.Release(ctx)
}

func (mr *sharedRef) Sys() Reference {
	sys := mr.Reference
	if s, ok := sys.(interface {
		Sys() Reference
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
		return r.sharedRef.Reference.Release(ctx)
	}
	return nil
}

func originRef(ref Reference) Reference {
	sysRef := ref
	if sys, ok := ref.(interface {
		Sys() Reference
	}); ok {
		sysRef = sys.Sys()
	}
	return sysRef
}

func toImmutableRef(ref Reference) (cache.ImmutableRef, bool) {
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
