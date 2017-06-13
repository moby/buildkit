package solver

import (
	"context"
	"sync"

	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
	"github.com/tonistiigi/buildkit_poc/client"
	"github.com/tonistiigi/buildkit_poc/util/progress"
)

type jobs struct {
	mu   sync.RWMutex
	refs map[string]*job
}

func (j *jobs) new(id string, g *opVertex, pr progress.ProgressReader) (*job, error) {
	j.mu.Lock()
	defer j.mu.Unlock()

	if j.refs == nil {
		j.refs = make(map[string]*job)
	}
	if _, ok := j.refs[id]; ok {
		return nil, errors.Errorf("id %s exists", id)
	}
	nj := &job{g: g, pr: progress.NewMultiReader(pr)}
	j.refs[id] = nj

	go func() {
		j.mu.Lock()
		defer j.mu.Unlock()
		delete(j.refs, id)
	}()

	return j.refs[id], nil
}

func (j *jobs) get(id string) (*job, error) {
	j.mu.RLock()
	defer j.mu.RUnlock()
	nj, ok := j.refs[id]
	if !ok {
		return nil, errors.Errorf("no such job %s", id)
	}
	return nj, nil
}

type job struct {
	mu sync.Mutex
	g  *opVertex
	pr *progress.MultiReader
}

func (j *job) pipe(ctx context.Context, ch chan *client.SolveStatus) error {
	pr := j.pr.Reader(ctx)
	for v := range flatten(j.g) {
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
		p, err := pr.Read(ctx) // add cancelling
		if err != nil {
			return err
		}
		switch v := p.Sys.(type) {
		case *client.Vertex:
			ss := &client.SolveStatus{Vertexes: []*client.Vertex{v}}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case ch <- ss:
			}
		}
	}

	return nil
}

func flatten(op *opVertex) chan *opVertex {
	cache := make(map[digest.Digest]struct{})
	ch := make(chan *opVertex, 32)
	go sendVertex(ch, op, cache)
	return ch
}

func sendVertex(ch chan *opVertex, op *opVertex, cache map[digest.Digest]struct{}) {
	for _, v := range op.inputs {
		sendVertex(ch, v, cache)
	}
	if _, ok := cache[op.dgst]; !ok {
		ch <- op
		cache[op.dgst] = struct{}{}
	}
}
