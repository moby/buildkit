package solver

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/util/flightcontrol"
	"github.com/moby/buildkit/util/progress"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
)

// ResolveOpFunc finds an Op implementation for a Vertex
type ResolveOpFunc func(Vertex) (Op, error)

// JobList provides a shared graph of all the vertexes currently being
// processed. Every vertex that is being solved needs to be loaded into job
// first. Vertex operations are invoked and progress tracking happends through
// jobs.
// TODO: s/JobList/Solver
type JobList struct {
	mu      sync.RWMutex
	jobs    map[string]*Job
	actives map[digest.Digest]*state
	opts    SolverOpt

	updateCond *sync.Cond
	s          *Scheduler
	index      *EdgeIndex
}

type state struct {
	jobs     map[*Job]struct{}
	parents  map[digest.Digest]struct{}
	childVtx map[digest.Digest]struct{}

	mpw   *progress.MultiWriter
	allPw map[progress.Writer]struct{}

	vtx          Vertex
	clientVertex client.Vertex

	mu    sync.Mutex
	op    *sharedOp
	edges map[Index]*edge
	opts  SolverOpt
	index *EdgeIndex
}

func (s *state) getEdge(index Index) *edge {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.edges[index]; ok {
		return e
	}

	if s.op == nil {
		s.op = newSharedOp(s.opts.ResolveOpFunc, s.opts.DefaultCache, s)
	}

	e := newEdge(Edge{Index: index, Vertex: s.vtx}, s.op, s.index)
	s.edges[index] = e
	return e
}

func (s *state) setEdge(index Index, newEdge *edge) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.edges[index]
	if ok {
		if e == newEdge {
			return
		}
		e.release()
	}

	newEdge.incrementReferenceCount()
	s.edges[index] = newEdge
}

func (s *state) Release() {
	for _, e := range s.edges {
		e.release()
	}
}

type Job struct {
	list *JobList
	pr   *progress.MultiReader
	pw   progress.Writer

	progressCloser func()
}

type SolverOpt struct {
	ResolveOpFunc ResolveOpFunc
	DefaultCache  CacheManager
}

func NewJobList(opts SolverOpt) *JobList {
	if opts.DefaultCache == nil {
		opts.DefaultCache = NewInMemoryCacheManager()
	}
	jl := &JobList{
		jobs:    make(map[string]*Job),
		actives: make(map[digest.Digest]*state),
		opts:    opts,
		index:   NewEdgeIndex(),
	}
	jl.s = NewScheduler(jl)
	jl.updateCond = sync.NewCond(jl.mu.RLocker())
	return jl
}

func (jl *JobList) SetEdge(e Edge, newEdge *edge) {
	jl.mu.RLock()
	defer jl.mu.RUnlock()

	st, ok := jl.actives[e.Vertex.Digest()]
	if !ok {
		return
	}

	st.setEdge(e.Index, newEdge)
}

func (jl *JobList) GetEdge(e Edge) *edge {
	jl.mu.RLock()
	defer jl.mu.RUnlock()

	st, ok := jl.actives[e.Vertex.Digest()]
	if !ok {
		return nil
	}
	return st.getEdge(e.Index)
}

func (jl *JobList) SubBuild(ctx context.Context, e Edge, parent Vertex) (CachedResult, error) {
	if err := jl.load(e.Vertex, parent, nil); err != nil {
		return nil, err
	}

	return jl.s.build(ctx, e)
}

func (jl *JobList) Close() {
	jl.s.Stop()
}

func (jl *JobList) load(v, parent Vertex, j *Job) error {
	jl.mu.Lock()
	defer jl.mu.Unlock()
	return jl.loadUnlocked(v, parent, j)
}

func (jl *JobList) loadUnlocked(v, parent Vertex, j *Job) error {
	for _, e := range v.Inputs() {
		if err := jl.loadUnlocked(e.Vertex, parent, j); err != nil {
			return err
		}
	}

	dgst := v.Digest()

	st, ok := jl.actives[dgst]
	if !ok {
		st = &state{
			opts:         jl.opts,
			jobs:         map[*Job]struct{}{},
			parents:      map[digest.Digest]struct{}{},
			childVtx:     map[digest.Digest]struct{}{},
			allPw:        map[progress.Writer]struct{}{},
			mpw:          progress.NewMultiWriter(progress.WithMetadata("vertex", dgst)),
			vtx:          v,
			clientVertex: initClientVertex(v),
			edges:        map[Index]*edge{},
			index:        jl.index,
		}
		jl.actives[dgst] = st
	}

	if j != nil {
		if _, ok := st.jobs[j]; !ok {
			st.jobs[j] = struct{}{}
		}
	}

	if parent != nil {
		if _, ok := st.parents[parent.Digest()]; !ok {
			st.parents[parent.Digest()] = struct{}{}
			parentState, ok := jl.actives[parent.Digest()]
			if !ok {
				return errors.Errorf("inactive parent %s", parent.Digest())
			}
			parentState.childVtx[dgst] = struct{}{}
		}
	}

	jl.connectProgressFromState(st, st)
	return nil
}

func (jl *JobList) connectProgressFromState(target, src *state) {
	for j := range src.jobs {
		if _, ok := target.allPw[j.pw]; !ok {
			target.mpw.Add(j.pw)
			target.allPw[j.pw] = struct{}{}
			j.pw.Write(target.clientVertex.Digest.String(), target.clientVertex)
		}
	}
	for p := range src.parents {
		jl.connectProgressFromState(target, jl.actives[p])
	}
}

func (jl *JobList) NewJob(id string) (*Job, error) {
	jl.mu.Lock()
	defer jl.mu.Unlock()

	if _, ok := jl.jobs[id]; ok {
		return nil, errors.Errorf("job ID %s exists", id)
	}

	pr, ctx, progressCloser := progress.NewContext(context.Background())
	pw, _, _ := progress.FromContext(ctx) // TODO: expose progress.Pipe()

	j := &Job{
		list:           jl,
		pr:             progress.NewMultiReader(pr),
		pw:             pw,
		progressCloser: progressCloser,
	}
	jl.jobs[id] = j

	jl.updateCond.Broadcast()

	return j, nil
}

func (jl *JobList) Get(id string) (*Job, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	go func() {
		<-ctx.Done()
		jl.updateCond.Broadcast()
	}()

	jl.mu.RLock()
	defer jl.mu.RUnlock()
	for {
		select {
		case <-ctx.Done():
			return nil, errors.Errorf("no such job %s", id)
		default:
		}
		j, ok := jl.jobs[id]
		if !ok {
			jl.updateCond.Wait()
			continue
		}
		return j, nil
	}
}

// called with joblist lock
func (jl *JobList) deleteIfUnreferenced(k digest.Digest, st *state) {
	if len(st.jobs) == 0 && len(st.parents) == 0 {
		for chKey := range st.childVtx {
			chState := jl.actives[chKey]
			delete(chState.parents, k)
			jl.deleteIfUnreferenced(chKey, chState)
		}
		st.Release()
		delete(jl.actives, k)
	}
}

func (j *Job) Build(ctx context.Context, e Edge) (CachedResult, error) {
	if err := j.list.load(e.Vertex, nil, j); err != nil {
		return nil, err
	}
	return j.list.s.build(ctx, e)
}

func (j *Job) Discard() error {
	defer j.progressCloser()

	j.list.mu.Lock()
	defer j.list.mu.Unlock()

	j.pw.Close()

	for k, st := range j.list.actives {
		if _, ok := st.jobs[j]; ok {
			delete(st.jobs, j)
			j.list.deleteIfUnreferenced(k, st)
		}
		if _, ok := st.allPw[j.pw]; ok {
			delete(st.allPw, j.pw)
		}
	}
	return nil
}

type activeOp interface {
	Op
	Cache() CacheManager
	CalcSlowCache(context.Context, Index, ResultBasedCacheFunc, Result) (digest.Digest, error)
}

func newSharedOp(resolver ResolveOpFunc, cacheManager CacheManager, st *state) *sharedOp {
	so := &sharedOp{
		resolver:     resolver,
		st:           st,
		cacheManager: cacheManager,
		slowCacheRes: map[Index]digest.Digest{},
		slowCacheErr: map[Index]error{},
	}
	return so
}

type sharedOp struct {
	resolver     ResolveOpFunc
	cacheManager CacheManager
	st           *state
	g            flightcontrol.Group

	opOnce sync.Once
	op     Op
	err    error

	execRes []*SharedResult
	execErr error

	cacheRes *CacheMap
	cacheErr error

	slowMu       sync.Mutex
	slowCacheRes map[Index]digest.Digest
	slowCacheErr map[Index]error
}

func (s *sharedOp) Cache() CacheManager {
	return s.cacheManager // TODO: add on load
}

func (s *sharedOp) CalcSlowCache(ctx context.Context, index Index, f ResultBasedCacheFunc, res Result) (digest.Digest, error) {
	key, err := s.g.Do(ctx, fmt.Sprintf("slow-compute-%d", index), func(ctx context.Context) (interface{}, error) {
		s.slowMu.Lock()
		// TODO: add helpers for these stored values
		if res := s.slowCacheRes[index]; res != "" {
			s.slowMu.Unlock()
			return res, nil
		}
		if err := s.slowCacheErr[index]; err != nil {
			s.slowMu.Unlock()
			return err, nil
		}
		s.slowMu.Unlock()
		ctx = progress.WithProgress(ctx, s.st.mpw)
		key, err := f(ctx, res)
		complete := true
		if err != nil {
			canceled := false
			select {
			case <-ctx.Done():
				canceled = true
			default:
			}
			if canceled && errors.Cause(err) == context.Canceled {
				complete = false
			}
		}
		s.slowMu.Lock()
		defer s.slowMu.Unlock()
		if complete {
			if err == nil {
				s.slowCacheRes[index] = key
			}
			s.slowCacheErr[index] = err
		}
		return key, err
	})
	if err != nil {
		return "", err
	}
	return key.(digest.Digest), nil
}

func (s *sharedOp) CacheMap(ctx context.Context) (*CacheMap, error) {
	op, err := s.getOp()
	if err != nil {
		return nil, err
	}
	res, err := s.g.Do(ctx, "cachemap", func(ctx context.Context) (interface{}, error) {
		if s.cacheRes != nil {
			return s.cacheRes, nil
		}
		if s.cacheErr != nil {
			return nil, s.cacheErr
		}
		ctx = progress.WithProgress(ctx, s.st.mpw)
		res, err := op.CacheMap(ctx)
		complete := true
		if err != nil {
			canceled := false
			select {
			case <-ctx.Done():
				canceled = true
			default:
			}
			if canceled && errors.Cause(err) == context.Canceled {
				complete = false
			}
		}
		if complete {
			if err == nil {
				s.cacheRes = res
			}
			s.cacheErr = err
		}
		return res, err
	})
	if err != nil {
		return nil, err
	}
	return res.(*CacheMap), nil
}

func (s *sharedOp) Exec(ctx context.Context, inputs []Result) (outputs []Result, err error) {
	op, err := s.getOp()
	if err != nil {
		return nil, err
	}
	res, err := s.g.Do(ctx, "exec", func(ctx context.Context) (interface{}, error) {
		if s.execRes != nil || s.execErr != nil {
			return s.execRes, s.execErr
		}
		ctx = progress.WithProgress(ctx, s.st.mpw)
		res, err := op.Exec(ctx, inputs)
		complete := true
		if err != nil {
			canceled := false
			select {
			case <-ctx.Done():
				canceled = true
			default:
			}
			if canceled && errors.Cause(err) == context.Canceled {
				complete = false
			}
		}
		if complete {
			if res != nil {
				s.execRes = wrapShared(res)
			}
			s.execErr = err
		}
		return s.execRes, err
	})
	if err != nil {
		return nil, err
	}
	return unwrapShared(res.([]*SharedResult)), nil
}

func (s *sharedOp) getOp() (Op, error) {
	s.opOnce.Do(func() {
		s.op, s.err = s.resolver(s.st.vtx)
	})
	if s.err != nil {
		return nil, s.err
	}
	return s.op, nil
}

func (s *sharedOp) release() {
	if s.execRes != nil {
		for _, r := range s.execRes {
			r.Release(context.TODO())
		}
	}
}

func initClientVertex(v Vertex) client.Vertex {
	inputDigests := make([]digest.Digest, 0, len(v.Inputs()))
	for _, inp := range v.Inputs() {
		inputDigests = append(inputDigests, inp.Vertex.Digest())
	}
	return client.Vertex{
		Inputs: inputDigests,
		Name:   v.Name(),
		Digest: v.Digest(),
	}
}

func wrapShared(inp []Result) []*SharedResult {
	out := make([]*SharedResult, len(inp))
	for i, r := range inp {
		out[i] = NewSharedResult(r)
	}
	return out
}

func unwrapShared(inp []*SharedResult) []Result {
	out := make([]Result, len(inp))
	for i, r := range inp {
		out[i] = r.Clone()
	}
	return out
}
