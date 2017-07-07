package solver

import (
	"sync"

	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/source"
	"golang.org/x/net/context"
)

type sourceOp struct {
	mu  sync.Mutex
	op  *pb.Op_Source
	sm  *source.Manager
	src source.SourceInstance
}

func newSourceOp(op *pb.Op_Source, sm *source.Manager) (Op, error) {
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
	id, err := source.FromString(s.op.Source.Identifier)
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

func (s *sourceOp) CacheKey(ctx context.Context, _ []string) (string, error) {
	src, err := s.instance(ctx)
	if err != nil {
		return "", err
	}
	return src.CacheKey(ctx)
}

func (s *sourceOp) Run(ctx context.Context, _ []Reference) ([]Reference, error) {
	src, err := s.instance(ctx)
	if err != nil {
		return nil, err
	}
	ref, err := src.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	return []Reference{ref}, nil
}
