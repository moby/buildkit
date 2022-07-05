// Package llbmutator defines the LLBMutator interface
package llbmutator

import (
	"context"

	"github.com/moby/buildkit/solver/pb"
)

type LLBMutator interface {
	Mutate(ctx context.Context, op *pb.Op) (bool, error)
}
