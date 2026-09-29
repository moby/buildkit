package base

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/moby/buildkit/cache/metadata"
	"github.com/moby/buildkit/util/db/boltutil"
	"github.com/moby/buildkit/util/db/compaction"
	"github.com/stretchr/testify/require"
	bolt "go.etcd.io/bbolt"
)

func TestCloseContentMetadata(t *testing.T) {
	root := t.TempDir()
	md, err := metadata.NewStore(filepath.Join(root, "metadata_v2.db"))
	require.NoError(t, err)
	path := filepath.Join(root, "containerdmeta.db")
	database, err := boltutil.Open(path, 0600, nil, compaction.DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, database.Update(func(*bolt.Tx) error { return nil }))
	w := &Worker{WorkerOpt: WorkerOpt{MetadataStore: md, ContentMetadata: database}}
	require.NoError(t, w.Close())
	data, err := os.ReadFile(path + ".compact-state")
	require.NoError(t, err)
	var state compaction.State
	require.NoError(t, json.Unmarshal(data, &state))
	require.Equal(t, uint64(1), state.Writes)
}

func TestID(t *testing.T) {
	t.Parallel()
	tmpdir := t.TempDir()

	id0, err := ID(tmpdir)
	require.NoError(t, err)

	id1, err := ID(tmpdir)
	require.NoError(t, err)

	require.Equal(t, id0, id1)

	// reset tmpdir
	require.NoError(t, os.RemoveAll(tmpdir))
	require.NoError(t, os.MkdirAll(tmpdir, 0700))

	id2, err := ID(tmpdir)
	require.NoError(t, err)

	require.NotEqual(t, id0, id2)
}
