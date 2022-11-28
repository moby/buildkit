package sourcepolicy

import (
	"context"

	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/bklog"
)

var DefaultMutater = MutateFn(Mutate)

// Mutater is the interface for mutating a source operation.
type Mutater interface {
	// Mutate mutates the source operation.
	// If the operation is not mutated, then the return value should be false.
	Mutate(ctx context.Context, op *pb.SourceOp, identifier string, attrs map[string]string) (bool, error)
}

// MutateFn wraps a function as a Mutater
type MutateFn func(ctx context.Context, op *pb.SourceOp, identifier string, attrs map[string]string) (bool, error)

// Mutate implements the Mutater interface for this function
func (fn MutateFn) Mutate(ctx context.Context, op *pb.SourceOp, identifier string, attrs map[string]string) (bool, error) {
	return fn(ctx, op, identifier, attrs)
}

// Mutate is a MutateFn which converts the source operation to the identifier and attributes provided by the policy.
// If there is no change, then the return value should be false and is not considered an error.
func Mutate(ctx context.Context, op *pb.SourceOp, identifier string, attrs map[string]string) (bool, error) {
	var mutated bool
	if op.Identifier != identifier && identifier != "" {
		mutated = true
		op.Identifier = identifier
	}

	if attrs != nil {
		if op.Attrs == nil {
			op.Attrs = make(map[string]string, len(attrs))
		}
		for k, v := range attrs {
			if op.Attrs[k] != v {
				bklog.G(ctx).Debugf("setting attr %s=%s", k, v)
				op.Attrs[k] = v
				mutated = true
			}
		}
	}

	return mutated, nil
}
