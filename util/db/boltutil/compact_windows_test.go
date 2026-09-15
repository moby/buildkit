package boltutil

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/moby/buildkit/util/db"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	bolt "go.etcd.io/bbolt"
	errbolt "go.etcd.io/bbolt/errors"
	"golang.org/x/sys/windows"
)

func TestCompactWindowsReplacementFailures(t *testing.T) {
	for _, tc := range []struct {
		name        string
		blockRename bool
		failReopen  bool
	}{
		{name: "rename", blockRename: true},
		{name: "reopen", failReopen: true},
		{name: "rename and reopen", blockRename: true, failReopen: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := openTestDB(t, filepath.Join(t.TempDir(), "test.db"))
			fillTestDB(t, d, 500)
			deleteTestKeys(t, d, 100, 500)
			if tc.blockRename {
				path, err := windows.UTF16PtrFromString(d.path)
				require.NoError(t, err)
				// Allow reads and writes, but deny replacement until this handle closes.
				h, err := windows.CreateFile(path, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, windows.CloseHandle(h)) })
			}
			reopenErr := errors.New("reopen failed")
			if tc.failReopen {
				d.opts.OpenFile = func(string, int, os.FileMode) (*os.File, error) {
					return nil, reopenErr
				}
			}
			res, err := d.Compact(t.Context(), db.CompactOptions{})
			require.Error(t, err)
			require.Equal(t, !tc.blockRename, res.Compacted)
			require.Equal(t, fileSize(t, d.path), res.SizeAfter)
			if tc.blockRename {
				require.True(t, errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_ACCESS_DENIED), "unexpected replacement error: %v", err)
				require.Equal(t, res.SizeBefore, res.SizeAfter)
			} else {
				require.Less(t, res.SizeAfter, res.SizeBefore)
			}
			require.NoFileExists(t, compactPath(d.path))
			if tc.failReopen {
				require.ErrorIs(t, err, reopenErr)
				require.ErrorIs(t, d.View(func(*bolt.Tx) error { return nil }), errbolt.ErrDatabaseNotOpen)
				require.NoError(t, d.Close())
				d = openTestDB(t, d.path)
			}
			checkTestDB(t, d, 0, 100, 500)
		})
	}
}
