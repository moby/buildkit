package solver

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	digest "github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/assert"
)

type mockVertex struct {
	dgst digest.Digest
	name string
}

func (v *mockVertex) Digest() digest.Digest  { return v.dgst }
func (v *mockVertex) Sys() any               { return nil }
func (v *mockVertex) Options() VertexOptions { return VertexOptions{} }
func (v *mockVertex) Inputs() []Edge         { return nil }
func (v *mockVertex) Name() string           { return v.name }

type mockResult struct {
	id string
}

func (r *mockResult) ID() string                    { return r.id }
func (r *mockResult) Release(context.Context) error { return nil }
func (r *mockResult) Sys() any                      { return nil }
func (r *mockResult) Clone() Result                 { return r }

func TestFireOnVertexComplete_CallsRegisteredCallbacks(t *testing.T) {
	vtx := &mockVertex{dgst: "sha256:abc", name: "test-vertex"}
	st := &state{
		jobs: make(map[*Job]struct{}),
		vtx:  vtx,
	}
	so := &sharedOp{st: st}

	var called atomic.Int32
	var receivedVtx Vertex
	var receivedResults []Result
	var mu sync.Mutex

	j := &Job{}
	j.SetOnVertexComplete(func(v Vertex, results []Result) {
		called.Add(1)
		mu.Lock()
		receivedVtx = v
		receivedResults = results
		mu.Unlock()
	})
	st.jobs[j] = struct{}{}

	results := []Result{&mockResult{id: "res1"}}
	so.fireOnVertexComplete(results)

	assert.Equal(t, int32(1), called.Load())
	mu.Lock()
	assert.Equal(t, vtx, receivedVtx)
	assert.Len(t, receivedResults, 1)
	assert.Equal(t, "res1", receivedResults[0].ID())
	mu.Unlock()
}

func TestFireOnVertexComplete_MultipleJobs(t *testing.T) {
	vtx := &mockVertex{dgst: "sha256:def", name: "multi-vertex"}
	st := &state{
		jobs: make(map[*Job]struct{}),
		vtx:  vtx,
	}
	so := &sharedOp{st: st}

	var called atomic.Int32

	for range 3 {
		j := &Job{}
		j.SetOnVertexComplete(func(v Vertex, results []Result) {
			called.Add(1)
		})
		st.jobs[j] = struct{}{}
	}

	so.fireOnVertexComplete([]Result{&mockResult{id: "r"}})

	assert.Equal(t, int32(3), called.Load())
}

func TestFireOnVertexComplete_SkipsJobsWithoutCallback(t *testing.T) {
	vtx := &mockVertex{dgst: "sha256:ghi", name: "skip-vertex"}
	st := &state{
		jobs: make(map[*Job]struct{}),
		vtx:  vtx,
	}
	so := &sharedOp{st: st}

	var called atomic.Int32

	jobWithCallback := &Job{}
	jobWithCallback.SetOnVertexComplete(func(v Vertex, results []Result) {
		called.Add(1)
	})
	jobWithoutCallback := &Job{}

	st.jobs[jobWithCallback] = struct{}{}
	st.jobs[jobWithoutCallback] = struct{}{}

	so.fireOnVertexComplete([]Result{&mockResult{id: "r"}})

	assert.Equal(t, int32(1), called.Load())
}

func TestFireOnVertexComplete_NoJobs(t *testing.T) {
	vtx := &mockVertex{dgst: "sha256:jkl", name: "noop-vertex"}
	st := &state{
		jobs: make(map[*Job]struct{}),
		vtx:  vtx,
	}
	so := &sharedOp{st: st}

	// Should not panic with no jobs
	so.fireOnVertexComplete([]Result{&mockResult{id: "r"}})
}

func TestSetOnVertexComplete(t *testing.T) {
	j := &Job{}
	assert.Nil(t, j.onVertexComplete)

	called := false
	j.SetOnVertexComplete(func(v Vertex, results []Result) {
		called = true
	})
	assert.NotNil(t, j.onVertexComplete)

	j.onVertexComplete(nil, nil)
	assert.True(t, called)
}
