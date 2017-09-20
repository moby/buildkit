package solver

import (
	"io"
	"sync"
	"time"

	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/session"
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
	jobs   map[*job]struct{}
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

	j := &job{l: jl, pr: progress.NewMultiReader(pr), pw: pw, session: sid, cache: cache}
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

func (jl *jobList) loadAndSolveChildVertex(ctx context.Context, dgst digest.Digest, vv *vertex, index Index, f ResolveOpFunc, cache InstructionCache) (Reference, error) {
	jl.mu.Lock()

	st, ok := jl.actives[dgst]
	if !ok {
		jl.mu.Unlock()
		return nil, errors.Errorf("no such parent vertex: %v", dgst)
	}

	var newst *state
	for j := range st.jobs {
		var err error
		newst, err = j.loadInternal(vv, f)
		if err != nil {
			jl.mu.Unlock()
			return nil, err
		}
	}
	jl.mu.Unlock()

	return getRef(newst.solver, ctx, vv, index, cache)
}

type job struct {
	l       *jobList
	pr      *progress.MultiReader
	pw      progress.Writer
	session string
	cache   InstructionCache
}

func (j *job) load(v *vertex, f ResolveOpFunc) error {
	j.l.mu.Lock()
	defer j.l.mu.Unlock()

	_, err := j.loadInternal(v, f)
	return err
}

func (j *job) loadInternal(v *vertex, f ResolveOpFunc) (*state, error) {
	for _, inp := range v.inputs {
		if _, err := j.loadInternal(inp.vertex, f); err != nil {
			return nil, err
		}
	}

	dgst := v.Digest()
	st, ok := j.l.actives[dgst]
	if !ok {
		st = &state{
			jobs: map[*job]struct{}{},
			mpw:  progress.NewMultiWriter(progress.WithMetadata("vertex", dgst)),
		}
		op, err := f(v)
		if err != nil {
			return nil, err
		}
		ctx := progress.WithProgress(context.Background(), st.mpw)
		ctx = session.NewContext(ctx, j.session) // TODO: support multiple

		s, err := newVertexSolver(ctx, v, op, j.cache, j.getSolver)
		if err != nil {
			return nil, err
		}
		st.solver = s

		j.l.actives[dgst] = st
	}
	if _, ok := st.jobs[j]; !ok {
		j.pw.Write(v.Digest().String(), v.clientVertex)
		st.mpw.Add(j.pw)
		st.jobs[j] = struct{}{}
	}
	return st, nil
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
	return getRef(s, ctx, v, index, j.cache)
}

func getRef(s VertexSolver, ctx context.Context, v *vertex, index Index, cache InstructionCache) (Reference, error) {
	k, err := s.CacheKey(ctx, index)
	if err != nil {
		return nil, err
	}
	ref, err := cache.Lookup(ctx, k)
	if err != nil {
		return nil, err
	}
	if ref != nil {
		v.notifyCompleted(ctx, true, nil)
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
				v.notifyCompleted(ctx, true, nil)
				return ref.(Reference), nil
			}
			continue
		}
		return r.Reference, nil
	}
}

func (j *job) pipe(ctx context.Context, ch chan *client.SolveStatus) error {
	pr := j.pr.Reader(ctx)
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
				ss.Vertexes = append(ss.Vertexes, &v)

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
