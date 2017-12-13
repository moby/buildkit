package solver

import (
	"io"

	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/executor"
	"github.com/moby/buildkit/frontend"
	"github.com/moby/buildkit/solver/reference"
	"github.com/moby/buildkit/worker"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
	"golang.org/x/net/context"
)

// llbBridge is an helper used by frontends
type llbBridge struct {
	*Solver
	job *job
	// this worker is used for running containerized frontend, not vertices
	worker *worker.Worker
}

type resolveImageConfig interface {
	ResolveImageConfig(ctx context.Context, ref string) (digest.Digest, []byte, error)
}

func (s *llbBridge) Solve(ctx context.Context, req frontend.SolveRequest) (cache.ImmutableRef, map[string][]byte, error) {
	var f frontend.Frontend
	if req.Frontend != "" {
		var ok bool
		f, ok = s.frontends[req.Frontend]
		if !ok {
			return nil, nil, errors.Errorf("invalid frontend: %s", req.Frontend)
		}
	} else {
		if req.Definition == nil || req.Definition.Def == nil {
			return nil, nil, nil
		}
	}
	ref, exp, err := s.solve(ctx, s.job, SolveRequest{
		Definition:  req.Definition,
		Frontend:    f,
		FrontendOpt: req.FrontendOpt,
	})
	if err != nil {
		return nil, nil, err
	}
	immutable, ok := reference.ToImmutableRef(ref)
	if !ok {
		return nil, nil, errors.Errorf("invalid reference for exporting: %T", ref)
	}
	return immutable, exp, nil
}

func (s *llbBridge) ResolveImageConfig(ctx context.Context, ref string) (digest.Digest, []byte, error) {
	// ImageSource is typically source/containerimage
	resolveImageConfig, ok := s.worker.ImageSource.(resolveImageConfig)
	if !ok {
		return "", nil, errors.Errorf("worker %q does not implement ResolveImageConfig", s.worker.Name)
	}
	return resolveImageConfig.ResolveImageConfig(ctx, ref)
}

func (s *llbBridge) Exec(ctx context.Context, meta executor.Meta, rootFS cache.ImmutableRef, stdin io.ReadCloser, stdout, stderr io.WriteCloser) error {
	active, err := s.worker.CacheManager.New(ctx, rootFS)
	if err != nil {
		return err
	}
	defer active.Release(context.TODO())
	return s.worker.Executor.Exec(ctx, meta, active, nil, stdin, stdout, stderr)
}
