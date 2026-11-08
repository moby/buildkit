package bboltcachestorage

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/solver/testutil"
	"github.com/moby/buildkit/util/db/compaction"
	digest "github.com/opencontainers/go-digest"
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

// TestReleaseThroughLinkOnlyKey checks that releasing the last result of a
// chain also collects keys above a key that only has links (no results).
func TestReleaseThroughLinkOnlyKey(t *testing.T) {
	st, err := NewStore(filepath.Join(t.TempDir(), "cache.db"))
	require.NoError(t, err)
	defer st.Close()

	l := func(s string) solver.CacheInfoLink {
		return solver.CacheInfoLink{Digest: digest.FromString(s)}
	}
	// top -> mid -> leaf, only leaf has a result
	require.NoError(t, st.AddLink("top", l("a"), "mid"))
	require.NoError(t, st.AddLink("mid", l("b"), "leaf"))
	require.NoError(t, st.AddResult("leaf", solver.CacheResult{ID: "res", CreatedAt: time.Now()}))

	require.NoError(t, st.Release("res"))

	for _, id := range []string{"top", "mid", "leaf"} {
		require.False(t, st.Exists(id), "%s should have been released", id)
	}
}
