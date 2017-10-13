package solver

import (
	"io"
	"sync"
	"time"

	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/solver/pb"
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

func (jl *jobList) new(ctx context.Context, id string, pr progress.Reader, cache InstructionCache) (context.Context, *job, error) {
	jl.mu.Lock()
	defer jl.mu.Unlock()

	if _, ok := jl.refs[id]; ok {
		return nil, nil, errors.Errorf("id %s exists", id)
	}

	pw, _, _ := progress.FromContext(ctx) // TODO: remove this
	sid := session.FromContext(ctx)

	j := &job{l: jl, pr: progress.NewMultiReader(pr), pw: pw, session: sid, cache: cache, cached: map[string]*cacheRecord{}}
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
	cache   InstructionCache
	cached  map[string]*cacheRecord
}

type cacheRecord struct {
	VertexSolver
	index Index
	ref   Reference
}

func (j *job) load(def *pb.Definition, resolveOp ResolveOpFunc) (*Input, error) {
	j.l.mu.Lock()
	defer j.l.mu.Unlock()

	return j.loadInternal(def, resolveOp)
}

func (j *job) loadInternal(def *pb.Definition, resolveOp ResolveOpFunc) (*Input, error) {
	vtx, idx, err := loadLLB(def, func(dgst digest.Digest, op *pb.Op, load func(digest.Digest) (interface{}, error)) (interface{}, error) {
		if st, ok := j.l.actives[dgst]; ok {
			if vtx, ok := st.jobs[j]; ok {
				return vtx, nil
			}
		}
		opMetadata := def.Metadata[dgst]
		vtx, err := newVertex(dgst, op, &opMetadata, load)
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
	return &Input{Vertex: vtx.(*vertex), Index: idx}, nil
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

func (j *job) getRef(ctx context.Context, v *vertex, index Index) (Reference, error) {
	s, err := j.getSolver(v.Digest())
	if err != nil {
		return nil, err
	}
	ref, err := getRef(s, ctx, v, index, j.cache)
	if err != nil {
		return nil, err
	}
	j.keepCacheRef(s, index, ref)
	return ref, nil
}

func (j *job) keepCacheRef(s VertexSolver, index Index, ref Reference) {
	immutable, ok := toImmutableRef(ref)
	if ok {
		j.cached[immutable.ID()] = &cacheRecord{s, index, ref}
	}
}

func (j *job) cacheExporter(ref Reference) (CacheExporter, error) {
	immutable, ok := toImmutableRef(ref)
	if !ok {
		return nil, errors.Errorf("invalid reference")
	}
	cr, ok := j.cached[immutable.ID()]
	if !ok {
		return nil, errors.Errorf("invalid cache exporter")
	}
	return cr.Cache(cr.index, cr.ref), nil
}

func getRef(s VertexSolver, ctx context.Context, v *vertex, index Index, cache InstructionCache) (Reference, error) {
	if v.metadata != nil && v.metadata.GetIgnoreCache() {
		logrus.Warnf("Unimplemented vertex metadata: IgnoreCache (%s, %s)", v.digest, v.name)
	}
	k, err := s.CacheKey(ctx, index)
	if err != nil {
		return nil, err
	}
	ref, err := cache.Lookup(ctx, k)
	if err != nil {
		return nil, err
	}
	if ref != nil {
		markCached(ctx, v.clientVertex)
		return ref.(Reference), nil
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
			ref, err := cache.Lookup(ctx, r.CacheKey)
			if err != nil {
				return nil, err
			}
			if ref != nil {
				markCached(ctx, v.clientVertex)
				return ref.(Reference), nil
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
