//go:build !windows

package boltutil

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/moby/buildkit/util/db"
	"github.com/stretchr/testify/require"
)

func TestCompactDirectorySyncFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires an unprivileged user to enforce directory permissions")
	}
	dir := t.TempDir()
	d := openTestDB(t, filepath.Join(dir, "test.db"))
	fillTestDB(t, d, 500)
	deleteTestKeys(t, d, 100, 500)
	// Permit replacement, but prevent opening the directory for fsync.
	require.NoError(t, os.Chmod(dir, 0300))
	t.Cleanup(func() { require.NoError(t, os.Chmod(dir, 0700)) })
	res, err := d.Compact(t.Context(), db.CompactOptions{})
	require.ErrorIs(t, err, os.ErrPermission)
	require.True(t, res.Compacted)
	require.Equal(t, fileSize(t, d.path), res.SizeAfter)
	require.Less(t, res.SizeAfter, res.SizeBefore)
	checkTestDB(t, d, 0, 100, 500)
}
