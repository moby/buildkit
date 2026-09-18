package bboltcachestorage

import (
	"path/filepath"
	"testing"

	"github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/solver/testutil"
	"github.com/moby/buildkit/util/db/compaction"
	"github.com/stretchr/testify/require"
)

func TestCompactionPolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.db")
	s, err := NewStore(path, compaction.DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, s.Close())
	require.FileExists(t, path+".compact-state")
}

func TestBoltCacheStorage(t *testing.T) {
	testutil.RunCacheStorageTests(t, func() solver.CacheKeyStorage {
		tmpDir := t.TempDir()

		st, err := NewStore(filepath.Join(tmpDir, "cache.db"))
		require.NoError(t, err)
		t.Cleanup(func() {
			require.NoError(t, st.Close())
		})

		return st
	})
}
