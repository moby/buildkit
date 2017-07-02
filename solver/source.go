package solver

import (
	"context"

	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/source"
)

func runSourceOp(ctx context.Context, sm *source.Manager, op *pb.Op_Source) ([]cache.ImmutableRef, error) {
	id, err := source.FromString(op.Source.Identifier)
	if err != nil {
		return nil, err
	}
	ref, err := sm.Pull(ctx, id)
	if err != nil {
		return nil, err
	}
	return []cache.ImmutableRef{ref}, nil
}
