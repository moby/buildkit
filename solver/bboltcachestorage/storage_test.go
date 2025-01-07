package bboltcachestorage

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/solver/testutil"
	"github.com/stretchr/testify/require"
)

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

func BenchmarkAddResult(b *testing.B) {
	tmpdir := b.TempDir()
	store, err := NewStore(filepath.Join(tmpdir, "cache.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()

	for i := 0; i < b.N; i++ {
		id, res := newResult()
		store.AddResult(id, res)
	}
}

func newResult() (string, solver.CacheResult) {
	return identity.NewID(), solver.CacheResult{
		CreatedAt: time.Now(),
		ID:        identity.NewID(),
	}
}
