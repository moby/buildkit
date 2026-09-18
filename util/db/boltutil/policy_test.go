package boltutil

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/moby/buildkit/util/db"
	"github.com/moby/buildkit/util/db/compaction"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	bolt "go.etcd.io/bbolt"
)

func readPolicy(t *testing.T, path string) compaction.State {
	t.Helper()
	data, err := os.ReadFile(path + ".compact-state")
	require.NoError(t, err)
	var state compaction.State
	require.NoError(t, json.Unmarshal(data, &state))
	return state
}

func TestOpenWithoutPolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	d, err := Open(path, 0600, nil)
	require.NoError(t, err)
	require.Nil(t, d.policy)
	require.NoError(t, d.Update(func(*bolt.Tx) error { return nil }))
	require.NoError(t, d.Close())
	_, err = os.Stat(path + ".compact-state")
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestSafeOpenPolicyFailurePreservesDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	d, err := Open(path, 0600, nil)
	require.NoError(t, err)
	require.NoError(t, d.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucket([]byte("keep"))
		return err
	}))
	require.NoError(t, d.Close())
	d, err = SafeOpen(path, 0600, nil, compaction.Config{})
	require.ErrorContains(t, err, "invalid database compaction policy")
	require.Nil(t, d)
	d, err = Open(path, 0600, nil)
	require.NoError(t, err)
	require.NoError(t, d.View(func(tx *bolt.Tx) error {
		require.NotNil(t, tx.Bucket([]byte("keep")))
		return nil
	}))
	require.NoError(t, d.Close())
	matches, err := filepath.Glob(path + ".*.bak")
	require.NoError(t, err)
	require.Empty(t, matches)
}

func TestPolicyPersistsCommittedWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	d, err := Open(path, 0600, nil, compaction.DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, d.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucket([]byte("data"))
		return err
	}))
	require.Error(t, d.Update(func(*bolt.Tx) error { return errors.New("rollback") }))
	require.Panics(t, func() { _ = d.Update(func(*bolt.Tx) error { panic("rollback") }) })
	require.NoError(t, d.Close())
	state := readPolicy(t, path)
	require.Equal(t, uint64(1), state.Writes)
	d, err = Open(path, 0600, nil, compaction.DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, d.Update(func(*bolt.Tx) error { return nil }))
	require.NoError(t, d.Close())
	require.Equal(t, uint64(2), readPolicy(t, path).Writes)
}

func TestPolicyStateFailurePreservesDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	d, err := Open(path, 0600, nil, compaction.DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, d.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucket([]byte("keep"))
		return err
	}))
	require.NoError(t, d.Close())
	require.NoError(t, os.WriteFile(path+".compact-state", []byte("invalid JSON"), 0600))
	safe, err := SafeOpen(path, 0600, nil, compaction.DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, safe.View(func(tx *bolt.Tx) error {
		require.NotNil(t, tx.Bucket([]byte("keep")))
		return nil
	}))
	require.NoError(t, safe.Close())
	matches, err := filepath.Glob(path + ".*.bak")
	require.NoError(t, err)
	require.Empty(t, matches)
}

func TestPolicyReadOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	d, err := Open(path, 0600, nil, compaction.DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, d.Close())
	require.NoError(t, os.Remove(path+".compact-state"))
	d, err = Open(path, 0600, &bolt.Options{ReadOnly: true}, compaction.DefaultConfig())
	require.NoError(t, err)
	require.Nil(t, d.policy)
	require.NoError(t, d.Close())
	_, err = os.Stat(path + ".compact-state")
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestNewDatabaseIgnoresOldPolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	old := compaction.State{Writes: 1000, WriteWatermark: 1 << 40}
	data, err := json.Marshal(old)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path+".compact-state", data, 0600))
	d, err := Open(path, 0600, nil, compaction.DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, d.Close())
	state := readPolicy(t, path)
	require.Zero(t, state.Writes)
	require.Equal(t, compaction.DefaultConfig().WriteWatermark, state.WriteWatermark)
}

func TestPolicyCompactsWithoutGC(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	cfg := compaction.DefaultConfig()
	cfg.WriteWatermark = 2
	cfg.MinReclaimBytes = 1
	cfg.IdleTimeout = 10 * time.Millisecond
	d, err := Open(path, 0600, nil, cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, d.Close()) })
	require.NoError(t, d.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucket([]byte("data"))
		if err != nil {
			return err
		}
		return b.Put([]byte("large"), make([]byte, 1<<20))
	}))
	before, err := os.Stat(path)
	require.NoError(t, err)
	require.NoError(t, d.Update(func(tx *bolt.Tx) error { return tx.Bucket([]byte("data")).Delete([]byte("large")) }))
	require.Eventually(t, func() bool {
		fi, err := os.Stat(path)
		return err == nil && fi.Size() < before.Size()
	}, 5*time.Second, 10*time.Millisecond)
	require.NoError(t, d.View(func(tx *bolt.Tx) error {
		require.NotNil(t, tx.Bucket([]byte("data")))
		return nil
	}))
	require.NoError(t, d.Close())
	data, err := os.ReadFile(path + ".compact-state")
	require.NoError(t, err)
	var state compaction.State
	require.NoError(t, json.Unmarshal(data, &state))
	require.Zero(t, state.Writes)
}

func TestPolicyIgnoresPersistedSizeWatermark(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	d := openTestDB(t, path)
	require.NoError(t, d.Close())
	require.NoError(t, os.WriteFile(path+".compact-state", []byte(`{"writes":123,"writeWatermark":200000,"sizeWatermark":9223372036854775807}`), 0600))
	d, err := Open(path, 0600, nil, compaction.DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, d.Close())
	require.Equal(t, compaction.State{Writes: 123, WriteWatermark: 200000}, readPolicy(t, path))
	data, err := os.ReadFile(path + ".compact-state")
	require.NoError(t, err)
	require.NotContains(t, string(data), "sizeWatermark")
}

func TestPolicySkipsPackedDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	d := openTestDB(t, path)
	fillTestDB(t, d, 100)
	res, err := d.Compact(t.Context(), db.CompactOptions{})
	require.NoError(t, err)
	require.True(t, res.Compacted)
	require.NoError(t, d.Close())
	cfg := compaction.DefaultConfig()
	cfg.WriteWatermark = 1
	cfg.MinReclaimBytes = 1 << 20
	cfg.IdleTimeout = time.Millisecond
	require.NoError(t, os.WriteFile(path+".compact-state", []byte(`{"writes":1}`), 0600))
	d, err = Open(path, 0600, nil, cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, d.Close()) })
	require.Never(t, func() bool {
		d.hmu.Lock()
		defer d.hmu.Unlock()
		return !d.lastCompact.IsZero()
	}, 50*time.Millisecond, time.Millisecond)
	require.NoError(t, d.Close())
	require.Equal(t, uint64(1), readPolicy(t, path).Writes)
}
