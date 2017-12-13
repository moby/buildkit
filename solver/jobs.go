package solver

import (
	"io"
	"sync"
	"time"

	"github.com/moby/buildkit/cache/instructioncache"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/solver/llbload"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/solver/reference"
	vtxpkg "github.com/moby/buildkit/solver/vertex"
	"github.com/moby/buildkit/util/progress"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"golang.org/x/net/context"
)

type jobKeyT string

var jobKey = jobKeyT("buildkit/solver/job")

type jobList struct {
	mu         sync.RWMutex
	refs       map[string]*job
	updateCond *sync.Cond
	actives    map[digest.Digest]*state
}

type state struct {
	jobs   map[*job]*vertex
	solver VertexSolver
	mpw    *progress.MultiWriter
}

func newJobList() *jobList {
	jl := &jobList{
		refs:    make(map[string]*job),
		actives: make(map[digest.Digest]*state),
	}
	jl.updateCond = sync.NewCond(jl.mu.RLocker())
	return jl
}

// jobInstructionCache implements InstructionCache.
// jobInstructionCache is instantiated for each of job instances rather than jobList or solver instances.
// Lookup for objects with IgnoreCache fail until Set is called.
type jobInstructionCache struct {
	mu sync.RWMutex
	instructioncache.InstructionCache
	ignoreCache  map[digest.Digest]struct{}
	setInThisJob map[digest.Digest]struct{}
}

// Probe implements InstructionCache
func (jic *jobInstructionCache) Probe(ctx context.Context, key digest.Digest) (bool, error) {
	jic.mu.RLock()
	defer jic.mu.RUnlock()
	_, ignoreCache := jic.ignoreCache[key]
	_, setInThisJob := jic.setInThisJob[key]
	if ignoreCache && !setInThisJob {
		return false, nil
	}
	return jic.InstructionCache.Probe(ctx, key)
}

// Lookup implements InstructionCache
func (jic *jobInstructionCache) Lookup(ctx context.Context, key digest.Digest, msg string) (interface{}, error) {
	jic.mu.RLock()
	defer jic.mu.RUnlock()
	_, ignoreCache := jic.ignoreCache[key]
	_, setInThisJob := jic.setInThisJob[key]
	if ignoreCache && !setInThisJob {
		return nil, nil
	}
	return jic.InstructionCache.Lookup(ctx, key, msg)
}

// Set implements InstructionCache
func (jic *jobInstructionCache) Set(key digest.Digest, ref interface{}) error {
	jic.mu.Lock()
	defer jic.mu.Unlock()
	jic.setInThisJob[key] = struct{}{}
	return jic.InstructionCache.Set(key, ref)
}

// SetIgnoreCache is jobInstructionCache-specific extension
func (jic *jobInstructionCache) SetIgnoreCache(key digest.Digest) {
	jic.mu.Lock()
	defer jic.mu.Unlock()
	jic.ignoreCache[key] = struct{}{}
}

func newJobInstructionCache(base instructioncache.InstructionCache) *jobInstructionCache {
	return &jobInstructionCache{
		InstructionCache: base,
		ignoreCache:      make(map[digest.Digest]struct{}),
		setInThisJob:     make(map[digest.Digest]struct{}),
	}
}

func (jl *jobList) new(ctx context.Context, id string, pr progress.Reader, cache instructioncache.InstructionCache) (context.Context, *job, error) {
	jl.mu.Lock()
	defer jl.mu.Unlock()

	if _, ok := jl.refs[id]; ok {
		return nil, nil, errors.Errorf("id %s exists", id)
	}

	pw, _, _ := progress.FromContext(ctx) // TODO: remove this
	sid := session.FromContext(ctx)

	// TODO(AkihiroSuda): find a way to integrate map[string]*cacheRecord to jobInstructionCache?
	j := &job{l: jl, pr: progress.NewMultiReader(pr), pw: pw, session: sid, cache: newJobInstructionCache(cache), cached: map[string]*cacheRecord{}}
	jl.refs[id] = j
	jl.updateCond.Broadcast()
	go func() {
		<-ctx.Done()
		jl.mu.Lock()
		defer jl.mu.Unlock()
		delete(jl.refs, id)
	}()

	return context.WithValue(ctx, jobKey, jl.refs[id]), jl.refs[id], nil
}

func (jl *jobList) get(id string) (*job, error) {
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
		j, ok := jl.refs[id]
		if !ok {
			jl.updateCond.Wait()
			continue
		}
		return j, nil
	}
}

type job struct {
	l       *jobList
	pr      *progress.MultiReader
	pw      progress.Writer
	session string
	cache   *jobInstructionCache
	cached  map[string]*cacheRecord
}

type cacheRecord struct {
	VertexSolver
	index vtxpkg.Index
	ref   reference.Ref
}

func (j *job) load(def *pb.Definition, resolveOp ResolveOpFunc) (*vtxpkg.Input, error) {
	j.l.mu.Lock()
	defer j.l.mu.Unlock()

	return j.loadInternal(def, resolveOp)
}

func (j *job) loadInternal(def *pb.Definition, resolveOp ResolveOpFunc) (*vtxpkg.Input, error) {
	vtx, idx, err := llbload.Load(def, func(dgst digest.Digest, pbOp *pb.Op, load func(digest.Digest) (interface{}, error)) (interface{}, error) {
		if st, ok := j.l.actives[dgst]; ok {
			if vtx, ok := st.jobs[j]; ok {
				return vtx, nil
			}
		}
		opMetadata := def.Metadata[dgst]
		vtx, err := newVertex(dgst, pbOp, &opMetadata, load)
		if err != nil {
			return nil, err
		}

		st, ok := j.l.actives[dgst]
		if !ok {
			st = &state{
				jobs: map[*job]*vertex{},
				mpw:  progress.NewMultiWriter(progress.WithMetadata("vertex", dgst)),
			}
			op, err := resolveOp(vtx)
			if err != nil {
				return nil, err
			}
			ctx := progress.WithProgress(context.Background(), st.mpw)
			ctx = session.NewContext(ctx, j.session) // TODO: support multiple

			s, err := newVertexSolver(ctx, vtx, op, j.cache, j.getSolver)
			if err != nil {
				return nil, err
			}
			for i, input := range pbOp.Inputs {
				if inputMetadata := def.Metadata[input.Digest]; inputMetadata.IgnoreCache {
					k, err := s.CacheKey(ctx, vtxpkg.Index(i))
					if err != nil {
						return nil, err
					}
					j.cache.SetIgnoreCache(k)
				}
			}
			st.solver = s

			j.l.actives[dgst] = st
		}
		if _, ok := st.jobs[j]; !ok {
			j.pw.Write(vtx.Digest().String(), vtx.clientVertex)
			st.mpw.Add(j.pw)
			st.jobs[j] = vtx
		}
		return vtx, nil
	})
	if err != nil {
		return nil, err
	}
	return &vtxpkg.Input{Vertex: vtx.(*vertex), Index: vtxpkg.Index(idx)}, nil
}

func (j *job) discard() {
	j.l.mu.Lock()
	defer j.l.mu.Unlock()

	j.pw.Close()

	for k, st := range j.l.actives {
		if _, ok := st.jobs[j]; ok {
			delete(st.jobs, j)
		}
		if len(st.jobs) == 0 {
			go st.solver.Release()
			delete(j.l.actives, k)
		}
	}
}

func (j *job) getSolver(dgst digest.Digest) (VertexSolver, error) {
	st, ok := j.l.actives[dgst]
	if !ok {
		return nil, errors.Errorf("vertex %v not found", dgst)
	}
	return st.solver, nil
}

func (j *job) getRef(ctx context.Context, cv client.Vertex, index vtxpkg.Index) (reference.Ref, error) {
	s, err := j.getSolver(cv.Digest)
	if err != nil {
		return nil, err
	}
	ref, err := getRef(ctx, s, cv, index, j.cache)
	if err != nil {
		return nil, err
	}
	j.keepCacheRef(s, index, ref)
	return ref, nil
}

func (j *job) keepCacheRef(s VertexSolver, index vtxpkg.Index, ref reference.Ref) {
	immutable, ok := reference.ToImmutableRef(ref)
	if ok {
		j.cached[immutable.ID()] = &cacheRecord{s, index, ref}
	}
}

func (j *job) cacheExporter(ref reference.Ref) (CacheExporter, error) {
	immutable, ok := reference.ToImmutableRef(ref)
	if !ok {
		return nil, errors.Errorf("invalid reference")
	}
	cr, ok := j.cached[immutable.ID()]
	if !ok {
		return nil, errors.Errorf("invalid cache exporter")
	}
	return cr.Cache(cr.index, cr.ref), nil
}

func getRef(ctx context.Context, s VertexSolver, cv client.Vertex, index vtxpkg.Index, cache instructioncache.InstructionCache) (reference.Ref, error) {
	k, err := s.CacheKey(ctx, index)
	if err != nil {
		return nil, err
	}
	ref, err := cache.Lookup(ctx, k, s.(*vertexSolver).v.Name())
	if err != nil {
		return nil, err
	}
	if ref != nil {
		markCached(ctx, cv)
		return ref.(reference.Ref), nil
	}

	ev, err := s.OutputEvaluator(index)
	if err != nil {
		return nil, err
	}
	defer ev.Cancel()

	for {
		r, err := ev.Next(ctx)
		if err != nil {
			return nil, err
		}
		if r.CacheKey != "" {
			ref, err := cache.Lookup(ctx, r.CacheKey, s.(*vertexSolver).v.Name())
			if err != nil {
				return nil, err
			}
			if ref != nil {
				markCached(ctx, cv)
				return ref.(reference.Ref), nil
			}
			continue
		}
		return r.Reference, nil
	}
}

func (j *job) pipe(ctx context.Context, ch chan *client.SolveStatus) error {
	vs := &vertexStream{cache: map[digest.Digest]*client.Vertex{}}
	pr := j.pr.Reader(ctx)
	defer func() {
		if enc := vs.encore(); len(enc) > 0 {
			ch <- &client.SolveStatus{Vertexes: enc}
		}
	}()
	for {
		p, err := pr.Read(ctx)
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		ss := &client.SolveStatus{}
		for _, p := range p {
			switch v := p.Sys.(type) {
			case client.Vertex:
				ss.Vertexes = append(ss.Vertexes, vs.append(v)...)

			case progress.Status:
				vtx, ok := p.Meta("vertex")
				if !ok {
					logrus.Warnf("progress %s status without vertex info", p.ID)
					continue
				}
				vs := &client.VertexStatus{
					ID:        p.ID,
					Vertex:    vtx.(digest.Digest),
					Name:      v.Action,
					Total:     int64(v.Total),
					Current:   int64(v.Current),
					Timestamp: p.Timestamp,
					Started:   v.Started,
					Completed: v.Completed,
				}
				ss.Statuses = append(ss.Statuses, vs)
			case client.VertexLog:
				vtx, ok := p.Meta("vertex")
				if !ok {
					logrus.Warnf("progress %s log without vertex info", p.ID)
					continue
				}
				v.Vertex = vtx.(digest.Digest)
				v.Timestamp = p.Timestamp
				ss.Logs = append(ss.Logs, &v)
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ch <- ss:
		}
	}
}

type vertexStream struct {
	cache map[digest.Digest]*client.Vertex
}

func (vs *vertexStream) append(v client.Vertex) []*client.Vertex {
	var out []*client.Vertex
	vs.cache[v.Digest] = &v
	if v.Cached {
		for _, inp := range v.Inputs {
			if inpv, ok := vs.cache[inp]; ok {
				if !inpv.Cached && inpv.Completed == nil {
					inpv.Cached = true
					inpv.Started = v.Completed
					inpv.Completed = v.Completed
					out = append(vs.append(*inpv), inpv)
				}
			}
		}
	}
	vcopy := v
	return append(out, &vcopy)
}

func (vs *vertexStream) encore() []*client.Vertex {
	var out []*client.Vertex
	for _, v := range vs.cache {
		if v.Started != nil && v.Completed == nil {
			now := time.Now()
			v.Completed = &now
			v.Error = context.Canceled.Error()
			out = append(out, v)
		}
	}
	return out
}
