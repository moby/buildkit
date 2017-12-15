package solver

import (
	"github.com/moby/buildkit/solver/types"
)

// Ref is a reference to the object passed through the build steps.
// This interface is a subset of the github.com/buildkit/buildkit/cache.Ref interface.
// For ease of unit testing, this interface only has Release().

type Ref = types.Ref
type Op = types.Op
type SolveRequest = types.SolveRequest
type Vertex = types.Vertex
type Input = types.Input
type Index = types.Index
