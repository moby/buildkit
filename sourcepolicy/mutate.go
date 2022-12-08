package sourcepolicy

import (
	"context"

	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/bklog"
)

// mutate is a MutateFn which converts the source operation to the identifier and attributes provided by the policy.
// If there is no change, then the return value should be false and is not considered an error.
func mutate(ctx context.Context, op *pb.SourceOp, identifier string, attrs map[string]string) (bool, error) {
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
