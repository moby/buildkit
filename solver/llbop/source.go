package llbop

import (
	"context"
	"sync"

	"github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/source"
	digest "github.com/opencontainers/go-digest"
)

const sourceCacheType = "buildkit.source.v0"

type sourceOp struct {
	mu  sync.Mutex
	op  *pb.Op_Source
	sm  *source.Manager
	src source.SourceInstance
}

func NewSourceOp(_ solver.Vertex, op *pb.Op_Source, sm *source.Manager) (solver.Op, error) {
	return &sourceOp{
		op: op,
		sm: sm,
	}, nil
}

func (s *sourceOp) instance(ctx context.Context) (source.SourceInstance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.src != nil {
		return s.src, nil
	}
	id, err := source.FromLLB(s.op)
	if err != nil {
		return nil, err
	}
	src, err := s.sm.Resolve(ctx, id)
	if err != nil {
		return nil, err
	}
	s.src = src
	return s.src, nil
}

func (s *sourceOp) CacheKey(ctx context.Context) (digest.Digest, error) {
	src, err := s.instance(ctx)
	if err != nil {
		return "", err
	}
	k, err := src.CacheKey(ctx)
	if err != nil {
		return "", err
	}
	return digest.FromBytes([]byte(sourceCacheType + ":" + k)), nil
}

func (s *sourceOp) Run(ctx context.Context, _ []solver.Ref) ([]solver.Ref, error) {
	src, err := s.instance(ctx)
	if err != nil {
		return nil, err
	}
	ref, err := src.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	return []solver.Ref{ref}, nil
}

func (s *sourceOp) ContentMask(context.Context) (digest.Digest, [][]string, error) {
	return "", nil, nil
}
