package solver

import (
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/source"
	"golang.org/x/net/context"
)

type sourceOp struct {
	op *pb.Op_Source
	sm *source.Manager
}

func newSourceOp(op *pb.Op_Source, sm *source.Manager) (Op, error) {
	return &sourceOp{
		op: op,
		sm: sm,
	}, nil
}

func (s *sourceOp) Run(ctx context.Context, _ []Reference) ([]Reference, error) {
	id, err := source.FromString(s.op.Source.Identifier)
	if err != nil {
		return nil, err
	}
	ref, err := s.sm.Pull(ctx, id)
	if err != nil {
		return nil, err
	}
	return []Reference{ref}, nil
}
