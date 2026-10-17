//go:build cgo && (linux || darwin)

package sqlitecachestorage

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/solver/testutil"
	"github.com/stretchr/testify/require"
)

func TestSqliteCacheStorage(t *testing.T) {
	testutil.RunSolverTests(t, testutil.SolverTestOptions{
		NewCacheManager: func(t *testing.T) solver.CacheManager {
			storage := newStore(t)
			return solver.NewCacheManager(t.Context(), "local", storage)
		},
	})
}

func newStore(t *testing.T) *Store {
	tmpDir, err := os.MkdirTemp("", "sqlite-test")
	require.NoError(t, err)

	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("keeping database in %s", tmpDir)
			return
		}
		_ = os.RemoveAll(tmpDir)
	})

	cachePath := filepath.Join(tmpDir, "cache.db")
	st, err := NewStore(t.Context(), cachePath, solver.NewInMemoryResultStorage())
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, st.Close())
	})
	return st
}
