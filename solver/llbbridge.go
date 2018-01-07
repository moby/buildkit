package solver

import (
	"io"
	"strings"

	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/executor"
	"github.com/moby/buildkit/frontend"
	"github.com/moby/buildkit/util/tracing"
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
	worker.Worker
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
	immutable, ok := ToImmutableRef(ref)
	if !ok {
		return nil, nil, errors.Errorf("invalid reference for exporting: %T", ref)
	}
	return immutable, exp, nil
}

func (s *llbBridge) Exec(ctx context.Context, meta executor.Meta, root cache.ImmutableRef, stdin io.ReadCloser, stdout, stderr io.WriteCloser) (err error) {
	span, ctx := tracing.StartSpan(ctx, strings.Join(meta.Args, " "))
	err = s.Worker.Exec(ctx, meta, root, stdin, stdout, stderr)
	tracing.FinishWithError(span, err)
	return err
}
