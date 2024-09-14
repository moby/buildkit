package sqlcachestorage

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/solver"
)

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
