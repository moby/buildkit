package solver_test

import (
	_ "crypto/sha256"
	"testing"

	"github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/solver/testutil"
)

func TestInMemoryCacheStorage(t *testing.T) {
	testutil.RunSolverTests(t, testutil.SolverTestOptions{
		NewCacheManager: func(t *testing.T) solver.CacheManager {
			return solver.NewInMemoryCacheManager()
		},
	})
}
