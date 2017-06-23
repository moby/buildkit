package solver

import (
	"context"
	"io"
	"sync"
	"time"

	"github.com/Sirupsen/logrus"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/util/progress"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
)

type jobList struct {
	mu         sync.RWMutex
	refs       map[string]*job
	updateCond *sync.Cond
}

func newJobList() *jobList {
	jl := &jobList{
		refs: make(map[string]*job),
	}
	jl.updateCond = sync.NewCond(jl.mu.RLocker())
	return jl
}

func (jl *jobList) new(ctx context.Context, id string, g *opVertex, pr progress.Reader) (*job, error) {
	jl.mu.Lock()
	defer jl.mu.Unlock()

	if _, ok := jl.refs[id]; ok {
		return nil, errors.Errorf("id %s exists", id)
	}
	j := &job{g: g, pr: progress.NewMultiReader(pr)}
	jl.refs[id] = j
	jl.updateCond.Broadcast()
	go func() {
		<-ctx.Done()
		jl.mu.Lock()
		defer jl.mu.Unlock()
		delete(jl.refs, id)
	}()

	return jl.refs[id], nil
}

func (jl *jobList) get(id string) (*job, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
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
	mu sync.Mutex
	g  *opVertex
	pr *progress.MultiReader
}

func (j *job) pipe(ctx context.Context, ch chan *client.SolveStatus) error {
	pr := j.pr.Reader(ctx)
	for v := range walk(j.g) {
		ss := &client.SolveStatus{
			Vertexes: []*client.Vertex{&v.vtx},
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ch <- ss:
		}
	}
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
					logrus.Warnf("progress %s status without vertex info", p.ID)
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

func walk(op *opVertex) chan *opVertex {
	cache := make(map[digest.Digest]struct{})
	ch := make(chan *opVertex, 32)

	var send func(op *opVertex)
	send = func(op *opVertex) {
		for _, v := range op.inputs {
			send(v)
		}
		if _, ok := cache[op.dgst]; !ok {
			ch <- op
			cache[op.dgst] = struct{}{}
		}
	}

	go func() {
		send(op)
		close(ch)
	}()
	return ch
}
