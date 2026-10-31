package boltutil

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
	bolt "go.etcd.io/bbolt"
)

func diskFreelist(t *testing.T, path string, pageSize int) (uint64, uint64) {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()
	var meta [72]byte
	var txid, freelist uint64
	for i := range 2 {
		_, err := f.ReadAt(meta[:], int64(i*pageSize))
		require.NoError(t, err)
		if id := binary.LittleEndian.Uint64(meta[64:72]); i == 0 || id > txid {
			txid = id
			freelist = binary.LittleEndian.Uint64(meta[48:56])
		}
	}
	return txid, freelist
}

func TestCloseCheckpointsFreelist(t *testing.T) {
	for _, noSync := range []bool{false, true} {
		t.Run("NoSync="+strconv.FormatBool(noSync), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "test.db")
			opts := &bolt.Options{NoFreelistSync: true, NoSync: noSync, FreelistType: bolt.FreelistMapType}
			d, err := Open(path, 0600, opts)
			require.NoError(t, err)
			pageSize := d.bdb.Info().PageSize
			require.NoError(t, d.Update(func(tx *bolt.Tx) error {
				_, err := tx.CreateBucket([]byte("items"))
				return err
			}))
			_, freelist := diskFreelist(t, path, pageSize)
			require.Equal(t, uint64(math.MaxUint64), freelist)
			require.NoError(t, d.Close())
			_, freelist = diskFreelist(t, path, pageSize)
			require.NotEqual(t, uint64(math.MaxUint64), freelist)

			d, err = Open(path, 0600, opts)
			require.NoError(t, err)
			require.True(t, d.bdb.NoFreelistSync)
			require.NoError(t, d.View(func(tx *bolt.Tx) error {
				require.NotNil(t, tx.Bucket([]byte("items")))
				return nil
			}))
			require.NoError(t, d.Update(func(*bolt.Tx) error { return nil }))
			_, freelist = diskFreelist(t, path, pageSize)
			require.Equal(t, uint64(math.MaxUint64), freelist)
			require.NoError(t, d.Close())
		})
	}
}

func TestCloseDoesNotWriteReadOnlyDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	d, err := Open(path, 0600, nil)
	require.NoError(t, err)
	pageSize := d.bdb.Info().PageSize
	require.NoError(t, d.Close())
	before, _ := diskFreelist(t, path, pageSize)

	d, err = Open(path, 0600, &bolt.Options{ReadOnly: true, NoFreelistSync: true})
	require.NoError(t, err)
	require.NoError(t, d.Close())
	after, _ := diskFreelist(t, path, pageSize)
	require.Equal(t, before, after)
}

func TestCloseDoesNotWriteSyncedFreelist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	d, err := Open(path, 0600, nil)
	require.NoError(t, err)
	pageSize := d.bdb.Info().PageSize
	before, _ := diskFreelist(t, path, pageSize)
	require.NoError(t, d.Close())
	after, _ := diskFreelist(t, path, pageSize)
	require.Equal(t, before, after)
}
