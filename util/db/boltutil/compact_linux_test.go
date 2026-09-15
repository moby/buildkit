package boltutil

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/moby/buildkit/util/db"
	"github.com/stretchr/testify/require"
	bolt "go.etcd.io/bbolt"
	"golang.org/x/sys/unix"
)

// Run with BUILDKIT_TEST_BOLTUTIL_DISK_DIR pointing to a dedicated 128 MiB tmpfs.
func TestCompactDiskFull(t *testing.T) {
	root := os.Getenv("BUILDKIT_TEST_BOLTUTIL_DISK_DIR")
	if root == "" {
		t.Skip("requires a dedicated bounded tmpfs")
	}
	root, err := filepath.EvalSymlinks(root)
	require.NoError(t, err)
	root, err = filepath.Abs(root)
	require.NoError(t, err)
	var fs unix.Statfs_t
	require.NoError(t, unix.Statfs(root, &fs))
	require.EqualValues(t, unix.TMPFS_MAGIC, fs.Type, "refusing to fill a non-tmpfs filesystem")
	require.EqualValues(t, 128<<20, fs.Blocks*uint64(fs.Bsize), "requires a 128 MiB filesystem")
	var dirStat, parentStat unix.Stat_t
	require.NoError(t, unix.Stat(root, &dirStat))
	require.NoError(t, unix.Stat(filepath.Dir(root), &parentStat))
	require.NotEqual(t, parentStat.Dev, dirStat.Dev, "scratch directory must be a mountpoint")
	t.Setenv("TMPDIR", root)

	for _, duringCopy := range []bool{false, true} {
		name := "precheck"
		if duringCopy {
			name = "copy"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			d := openTestDB(t, filepath.Join(dir, "test.db"))
			fillTestDB(t, d, 1000)
			deleteTestKeys(t, d, 100, 1000)
			before := fileSize(t, d.path)
			fill := func() string {
				t.Helper()
				path := filepath.Join(dir, "filler")
				f, err := os.Create(path)
				require.NoError(t, err)
				defer f.Close()
				buf := make([]byte, 1<<20)
				for {
					if _, err := f.Write(buf); err != nil {
						require.ErrorIs(t, err, unix.ENOSPC)
						return path
					}
				}
			}
			var res db.CompactResult
			var err error
			var filler string
			if duringCopy {
				ctx := &pauseCopyContext{
					Context: context.WithoutCancel(t.Context()),
					path:    compactPath(d.path), ready: make(chan struct{}), release: make(chan struct{}),
				}
				var once sync.Once
				unblock := func() { once.Do(func() { close(ctx.release) }) }
				done := make(chan struct{})
				go func() {
					res, err = d.Compact(ctx, db.CompactOptions{})
					close(done)
				}()
				t.Cleanup(func() { unblock(); <-done })
				select {
				case <-ctx.ready:
				case <-done:
					t.Fatalf("compaction returned before copying: %+v, %v", res, err)
				case <-time.After(10 * time.Second):
					t.Fatal("compaction did not reach copying")
				}
				filler = fill()
				unblock()
				<-done
				require.ErrorIs(t, err, unix.ENOSPC)
			} else {
				filler = fill()
				res, err = d.Compact(t.Context(), db.CompactOptions{})
				require.NoError(t, err)
				require.Equal(t, "insufficient free disk space for compacted copy", res.Reason)
			}
			require.False(t, res.Compacted)
			require.Equal(t, before, fileSize(t, d.path))
			require.NoFileExists(t, compactPath(d.path))
			checkTestDB(t, d, 0, 100, 1000)
			require.NoError(t, os.Remove(filler))
			require.NoError(t, d.Update(func(tx *bolt.Tx) error {
				return tx.Bucket([]byte(testBucket)).Put(testKey(0), testValue(0))
			}))
			res, err = d.Compact(t.Context(), db.CompactOptions{})
			require.NoError(t, err)
			require.True(t, res.Compacted, res.Reason)
			checkTestDB(t, d, 0, 100, 1000)
		})
	}
}
