package worker

import (
	"context"

	"github.com/moby/buildkit/cache"
	solver "github.com/moby/buildkit/solver-next"
)

func NewWorkerRefResult(ref cache.ImmutableRef, worker Worker) solver.Result {
	return &workerRefResult{&WorkerRef{ImmutableRef: ref, Worker: worker}}
}

type WorkerRef struct {
	ImmutableRef cache.ImmutableRef
	Worker       Worker
}

func (wr *WorkerRef) ID() string {
	return wr.Worker.ID() + "::" + wr.ImmutableRef.ID()
}

type workerRefResult struct {
	*WorkerRef
}

func (r *workerRefResult) Release(ctx context.Context) error {
	return r.ImmutableRef.Release(ctx)
}

func (r *workerRefResult) Sys() interface{} {
	return r.WorkerRef
}
