package reference

import (
	"golang.org/x/net/context"
)

// Ref is a reference to the object passed through the build steps.
// This interface is a subset of the github.com/buildkit/buildkit/cache.Ref interface.
// For ease of unit testing, this interface only has Release().
type Ref interface {
	Release(context.Context) error
}
