package boltutil

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/containerd/containerd/v2/core/leases"
	ctdmetadata "github.com/containerd/containerd/v2/core/metadata"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/containerd/v2/plugins/content/local"
	"github.com/moby/buildkit/util/db"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	bolt "go.etcd.io/bbolt"
	errbolt "go.etcd.io/bbolt/errors"
)

const (
	testBucket  = "items"
	testNested  = "nested"
	testValSize = 4 << 10
)

func openTestDB(t testing.TB, path string) *DB {
	t.Helper()
	d, err := Open(path, 0600, &bolt.Options{FreelistType: bolt.FreelistMapType})
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, d.Close())
	})
	return d
}

func testKey(i int) []byte {
	return fmt.Appendf(nil, "key-%06d", i)
}

func testValue(i int) []byte {
	return bytes.Repeat([]byte{byte(i)}, testValSize)
}

// fillTestDB writes n keys into the test bucket, a nested bucket with a few
// keys and explicit sequence numbers on both buckets.
func fillTestDB(t testing.TB, d db.Transactor, n int) {
	t.Helper()
	require.NoError(t, d.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists([]byte(testBucket))
		if err != nil {
			return err
		}
		if err := b.SetSequence(1234); err != nil {
			return err
		}
		nb, err := b.CreateBucketIfNotExists([]byte(testNested))
		if err != nil {
			return err
		}
		if err := nb.SetSequence(42); err != nil {
			return err
		}
		for i := range 3 {
			if err := nb.Put(testKey(i), []byte("nested")); err != nil {
				return err
			}
		}
		return nil
	}))
	for i := 0; i < n; i += 100 {
		require.NoError(t, d.Update(func(tx *bolt.Tx) error {
			b := tx.Bucket([]byte(testBucket))
			for j := i; j < min(i+100, n); j++ {
				if err := b.Put(testKey(j), testValue(j)); err != nil {
					return err
				}
			}
			return nil
		}))
	}
}

func deleteTestKeys(t testing.TB, d db.Transactor, from, to int) {
	t.Helper()
	require.NoError(t, d.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(testBucket))
		for i := from; i < to; i++ {
			if err := b.Delete(testKey(i)); err != nil {
				return err
			}
		}
		return nil
	}))
}

// checkTestDB verifies that keys in [from, to) are present with their
// values, that keys outside that range are absent, and that the nested
// bucket and sequences survived.
func checkTestDB(t *testing.T, d db.Transactor, from, to, total int) {
	t.Helper()
	require.NoError(t, d.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(testBucket))
		require.NotNil(t, b)
		require.Equal(t, uint64(1234), b.Sequence())
		for i := range total {
			v := b.Get(testKey(i))
			if i >= from && i < to {
				require.Equal(t, testValue(i), v, "key %d", i)
			} else {
				require.Nil(t, v, "key %d", i)
			}
		}
		nb := b.Bucket([]byte(testNested))
		require.NotNil(t, nb)
		require.Equal(t, uint64(42), nb.Sequence())
		for i := range 3 {
			require.Equal(t, []byte("nested"), nb.Get(testKey(i)))
		}
		return nil
	}))
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	require.NoError(t, err)
	return fi.Size()
}

func TestCompactReclaimsSpace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	d := openTestDB(t, path)

	const total = 2000
	fillTestDB(t, d, total)
	deleteTestKeys(t, d, 0, total-50)
	before := fileSize(t, path)

	res, err := d.Compact(t.Context(), db.CompactOptions{})
	require.NoError(t, err)
	require.True(t, res.Compacted, res.Reason)
	require.Equal(t, before, res.SizeBefore)
	require.Positive(t, res.Reclaimable)
	require.Less(t, res.SizeAfter, before/2)
	require.Equal(t, res.SizeAfter, fileSize(t, path))
	require.NoFileExists(t, compactPath(path))

	checkTestDB(t, d, total-50, total, total)

	// The database stays usable after the handle was replaced.
	require.NoError(t, d.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(testBucket)).Put(testKey(total-51), testValue(total-51))
	}))
	checkTestDB(t, d, total-51, total, total)
}

func TestCompactSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	d := openTestDB(t, path)

	const total = 500
	fillTestDB(t, d, total)
	deleteTestKeys(t, d, 100, total)

	res, err := d.Compact(t.Context(), db.CompactOptions{})
	require.NoError(t, err)
	require.True(t, res.Compacted, res.Reason)
	require.NoError(t, d.Close())

	d2 := openTestDB(t, path)
	checkTestDB(t, d2, 0, 100, total)
}

func TestCompactReopenedFreelist(t *testing.T) {
	for _, noFreelistSync := range []bool{false, true} {
		t.Run(fmt.Sprintf("NoFreelistSync=%t", noFreelistSync), func(t *testing.T) {
			testCompactReopenedFreelist(t, noFreelistSync)
		})
	}
}

func testCompactReopenedFreelist(t *testing.T, noFreelistSync bool) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	open := func() *DB {
		t.Helper()
		d, err := Open(path, 0600, &bolt.Options{FreelistType: bolt.FreelistMapType, NoFreelistSync: noFreelistSync})
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, d.Close()) })
		return d
	}
	d := open()
	fillTestDB(t, d, 500)
	deleteTestKeys(t, d, 100, 500)
	require.NoError(t, d.Close())
	reopened := open()
	res, err := reopened.Compact(t.Context(), db.CompactOptions{MinReclaimBytes: 1 << 20})
	require.NoError(t, err)
	require.True(t, res.Compacted, res.Reason)
	require.Less(t, res.SizeAfter, res.SizeBefore)
	require.Equal(t, noFreelistSync, reopened.bdb.NoFreelistSync)
	checkTestDB(t, reopened, 0, 100, 500)
	require.NoError(t, reopened.Close())
	checkTestDB(t, open(), 0, 100, 500)
}

func TestCompactAfterTransactionFailure(t *testing.T) {
	for _, writable := range []bool{false, true} {
		for _, panics := range []bool{false, true} {
			t.Run(fmt.Sprintf("writable=%t/panic=%t", writable, panics), func(t *testing.T) {
				d := openTestDB(t, filepath.Join(t.TempDir(), "test.db"))
				fillTestDB(t, d, 10)
				run := d.View
				if writable {
					run = d.Update
				}
				failure := errors.New("transaction failed")
				fn := func(tx *bolt.Tx) error {
					if writable {
						if err := tx.Bucket([]byte(testBucket)).Delete(testKey(0)); err != nil {
							return err
						}
					}
					if panics {
						panic(failure)
					}
					return failure
				}
				if panics {
					require.PanicsWithValue(t, failure, func() { _ = run(fn) })
				} else {
					require.ErrorIs(t, run(fn), failure)
				}
				res, err := d.Compact(t.Context(), db.CompactOptions{PauseTimeout: time.Second})
				require.NoError(t, err)
				require.True(t, res.Compacted, res.Reason)
				checkTestDB(t, d, 0, 10, 10)
			})
		}
	}
}

func TestCompactSkipsBelowThresholds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	d := openTestDB(t, path)

	fillTestDB(t, d, 200)
	deleteTestKeys(t, d, 0, 100)
	before := fileSize(t, path)

	res, err := d.Compact(t.Context(), db.CompactOptions{MinReclaimBytes: 1 << 40})
	require.NoError(t, err)
	require.False(t, res.Compacted)
	require.NotEmpty(t, res.Reason)
	require.Equal(t, before, res.SizeBefore)
	require.Equal(t, before, res.SizeAfter)
	require.Positive(t, res.Reclaimable)

	res, err = d.Compact(t.Context(), db.CompactOptions{MinReclaimPercent: 100})
	require.NoError(t, err)
	require.False(t, res.Compacted)
	require.NotEmpty(t, res.Reason)

	require.Equal(t, before, fileSize(t, path))
	checkTestDB(t, d, 100, 200, 200)
}

func TestCompactRespectsMinInterval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	d := openTestDB(t, path)

	fillTestDB(t, d, 200)
	deleteTestKeys(t, d, 0, 150)

	res, err := d.Compact(t.Context(), db.CompactOptions{MinInterval: time.Hour})
	require.NoError(t, err)
	require.True(t, res.Compacted, res.Reason)

	deleteTestKeys(t, d, 150, 200)
	res, err = d.Compact(t.Context(), db.CompactOptions{MinInterval: time.Hour})
	require.NoError(t, err)
	require.False(t, res.Compacted)
	require.Equal(t, "compaction attempted recently", res.Reason)

	res, err = d.Compact(t.Context(), db.CompactOptions{})
	require.NoError(t, err)
	require.True(t, res.Compacted, res.Reason)
}

func TestCompactConcurrentTransactions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	d := openTestDB(t, path)
	fillTestDB(t, d, 0)

	const (
		writers        = 4
		writesPerActor = 250
	)

	errCh := make(chan error, writers)
	for w := range writers {
		go func() {
			errCh <- func() error {
				for i := range writesPerActor {
					n := w*writesPerActor + i
					if err := d.Update(func(tx *bolt.Tx) error {
						return tx.Bucket([]byte(testBucket)).Put(testKey(n), testValue(n))
					}); err != nil {
						return err
					}
					if err := d.View(func(tx *bolt.Tx) error {
						if !bytes.Equal(testValue(n), tx.Bucket([]byte(testBucket)).Get(testKey(n))) {
							return errors.Errorf("key %d not visible after write", n)
						}
						return nil
					}); err != nil {
						return err
					}
				}
				return nil
			}()
		}()
	}

	compactions := 0
	finished := 0
	for finished < writers {
		res, err := d.Compact(t.Context(), db.CompactOptions{})
		require.NoError(t, err)
		if res.Compacted {
			compactions++
		}
		select {
		case err := <-errCh:
			require.NoError(t, err)
			finished++
		case <-time.After(5 * time.Millisecond):
		}
	}
	require.Positive(t, compactions)
	checkTestDB(t, d, 0, writers*writesPerActor, writers*writesPerActor)
}

func TestCompactSustainedReaders(t *testing.T) {
	for _, readers := range []int{4, 16, 64} {
		t.Run(fmt.Sprint(readers), func(t *testing.T) {
			d := openTestDB(t, filepath.Join(t.TempDir(), "test.db"))
			fillTestDB(t, d, 1000)
			stop := make(chan struct{})
			errs := make(chan error, readers)
			var ready, running sync.WaitGroup
			ready.Add(readers)
			t.Cleanup(func() {
				close(stop)
				running.Wait()
				close(errs)
				for err := range errs {
					require.NoError(t, err)
				}
			})
			key := testKey(0)
			read := func(tx *bolt.Tx) error {
				if tx.Bucket([]byte(testBucket)).Get(key) == nil {
					return errors.New("missing key")
				}
				return nil
			}
			for range readers {
				running.Go(func() {
					err := d.View(read)
					ready.Done()
					for err == nil {
						select {
						case <-stop:
							return
						default:
						}
						err = d.View(read)
					}
					errs <- err
				})
			}
			ready.Wait()
			res, err := d.Compact(t.Context(), db.CompactOptions{PauseTimeout: 5 * time.Second, CopyTimeout: 5 * time.Second})
			require.NoError(t, err)
			require.True(t, res.Compacted, res.Reason)
		})
	}
}

func TestCompactCopyTimeout(t *testing.T) {
	d := openTestDB(t, filepath.Join(t.TempDir(), "test.db"))
	fillTestDB(t, d, 100)
	res, err := d.Compact(t.Context(), db.CompactOptions{PauseTimeout: time.Hour, CopyTimeout: time.Nanosecond})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.False(t, res.Compacted)
	require.NoFileExists(t, compactPath(d.path))
	checkTestDB(t, d, 0, 100, 100)
	res, err = d.Compact(t.Context(), db.CompactOptions{PauseTimeout: time.Second})
	require.NoError(t, err)
	require.True(t, res.Compacted, res.Reason)
}

func TestSafeOpenFailureReturnsNil(t *testing.T) {
	d, err := SafeOpen(filepath.Join(t.TempDir(), "missing", "test.db"), 0600, nil)
	require.Error(t, err)
	if d != nil {
		t.Fatal("SafeOpen returned a non-nil interface on failure")
	}
}

func TestCompactCopyFailurePreservesDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	d := openTestDB(t, path)

	fillTestDB(t, d, 300)
	deleteTestKeys(t, d, 0, 100)
	before := fileSize(t, path)

	// Make the copy run out of space part way through.
	d.opts.MaxSize = 64 << 10
	_, err := d.Compact(t.Context(), db.CompactOptions{})
	require.Error(t, err)
	require.NoFileExists(t, compactPath(path))
	require.Equal(t, before, fileSize(t, path))
	checkTestDB(t, d, 100, 300, 300)

	d.opts.MaxSize = 0
	res, err := d.Compact(t.Context(), db.CompactOptions{})
	require.NoError(t, err)
	require.True(t, res.Compacted, res.Reason)
	checkTestDB(t, d, 100, 300, 300)
}

func TestCompactCreateFailurePreservesDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	d := openTestDB(t, path)

	fillTestDB(t, d, 300)
	deleteTestKeys(t, d, 0, 100)
	before := fileSize(t, path)

	// A non-empty directory in place of the copy cannot be removed or
	// opened as a database.
	require.NoError(t, os.Mkdir(compactPath(path), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(compactPath(path), "blocker"), []byte("x"), 0600))

	_, err := d.Compact(t.Context(), db.CompactOptions{})
	require.Error(t, err)
	require.Equal(t, before, fileSize(t, path))
	checkTestDB(t, d, 100, 300, 300)
	require.NoError(t, d.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(testBucket)).Put(testKey(0), testValue(0))
	}))
}

func TestCompactPauseTimeout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	d := openTestDB(t, path)

	fillTestDB(t, d, 300)
	deleteTestKeys(t, d, 0, 200)

	// Hold a read transaction open so compaction cannot become idle.
	release := make(chan struct{})
	entered := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		_ = d.View(func(*bolt.Tx) error {
			close(entered)
			<-release
			return nil
		})
	})
	<-entered

	res, err := d.Compact(t.Context(), db.CompactOptions{PauseTimeout: 50 * time.Millisecond})
	require.NoError(t, err)
	require.False(t, res.Compacted)
	require.Equal(t, "timed out waiting for in-flight transactions", res.Reason)

	// The gate reopened: new transactions proceed while the old one is
	// still running.
	require.NoError(t, d.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(testBucket)).Put(testKey(199), testValue(199))
	}))

	close(release)
	wg.Wait()

	res, err = d.Compact(t.Context(), db.CompactOptions{})
	require.NoError(t, err)
	require.True(t, res.Compacted, res.Reason)
	checkTestDB(t, d, 199, 300, 300)
}

func TestCompactAfterClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	d := openTestDB(t, path)
	require.NoError(t, d.Close())

	_, err := d.Compact(t.Context(), db.CompactOptions{})
	require.Error(t, err)
}

type pauseCopyContext struct {
	context.Context
	path    string
	ready   chan struct{}
	release chan struct{}
	once    sync.Once
}

func (c *pauseCopyContext) Err() error {
	if _, err := os.Stat(c.path); err == nil {
		c.once.Do(func() {
			close(c.ready)
			<-c.release
		})
	}
	return context.Cause(c.Context)
}

func TestCompactConcurrentMaintenance(t *testing.T) {
	for _, closeDB := range []bool{false, true} {
		t.Run(fmt.Sprintf("close=%t", closeDB), func(t *testing.T) {
			d := openTestDB(t, filepath.Join(t.TempDir(), "test.db"))
			fillTestDB(t, d, 100)
			ctx := &pauseCopyContext{
				Context: context.WithoutCancel(t.Context()),
				path:    compactPath(d.path), ready: make(chan struct{}), release: make(chan struct{}),
			}
			var release sync.Once
			unblock := func() { release.Do(func() { close(ctx.release) }) }
			t.Cleanup(unblock)
			done := make(chan error, 1)
			go func() {
				res, err := d.Compact(ctx, db.CompactOptions{})
				if err == nil && !res.Compacted {
					err = errors.Errorf("compaction skipped: %s", res.Reason)
				}
				done <- err
			}()
			<-ctx.ready
			fi, err := os.Stat(ctx.path)
			require.NoError(t, err)
			var closed chan error
			if closeDB {
				closed = make(chan error, 1)
				go func() { closed <- d.Close() }()
				select {
				case err := <-closed:
					t.Fatalf("Close returned before compaction finished: %v", err)
				case <-time.After(50 * time.Millisecond):
				}
			} else {
				res, err := d.Compact(t.Context(), db.CompactOptions{})
				require.NoError(t, err)
				require.False(t, res.Compacted)
				require.Equal(t, "database maintenance in progress", res.Reason)
				current, err := os.Stat(ctx.path)
				require.NoError(t, err)
				require.True(t, os.SameFile(fi, current))
			}
			unblock()
			require.NoError(t, <-done)
			if closeDB {
				require.NoError(t, <-closed)
				require.ErrorIs(t, d.View(func(*bolt.Tx) error { return nil }), errbolt.ErrDatabaseNotOpen)
				d = openTestDB(t, d.path)
			}
			require.NoFileExists(t, ctx.path)
			checkTestDB(t, d, 0, 100, 100)
		})
	}
}

func TestCompactCanceledContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	d := openTestDB(t, path)
	fillTestDB(t, d, 10)

	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(context.Canceled)
	_, err := d.Compact(ctx, db.CompactOptions{})
	require.ErrorIs(t, err, context.Canceled)
}

func TestOpenRemovesStaleCompactionFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	require.NoError(t, os.WriteFile(compactPath(path), []byte("stale"), 0600))

	openTestDB(t, path)
	require.NoFileExists(t, compactPath(path))
}

func TestCompactHeldWriter(t *testing.T) {
	d := openTestDB(t, filepath.Join(t.TempDir(), "test.db"))
	fillTestDB(t, d, 100)
	deleteTestKeys(t, d, 0, 50)
	release, entered := make(chan struct{}), make(chan struct{})
	writerDone := make(chan error, 1)
	go func() {
		writerDone <- d.Update(func(*bolt.Tx) error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	t.Cleanup(func() {
		close(release)
		require.NoError(t, <-writerDone)
	})
	done := make(chan error, 1)
	go func() {
		res, err := d.Compact(t.Context(), db.CompactOptions{PauseTimeout: 20 * time.Millisecond, MinInterval: time.Hour})
		if err == nil && res.Compacted {
			err = errors.New("compacted with an active writer")
		}
		done <- err
	}()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("compaction blocked behind a writer before applying its timeout")
	}
	res, err := d.Compact(t.Context(), db.CompactOptions{MinInterval: time.Hour})
	require.NoError(t, err)
	require.Equal(t, "compaction attempted recently", res.Reason)
}

func TestCompactCancelWhileDraining(t *testing.T) {
	d := openTestDB(t, filepath.Join(t.TempDir(), "test.db"))
	fillTestDB(t, d, 100)
	release, entered := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- d.View(func(*bolt.Tx) error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	t.Cleanup(func() {
		close(release)
		require.NoError(t, <-done)
	})
	ctx, cancel := context.WithTimeoutCause(t.Context(), 20*time.Millisecond, context.Canceled)
	defer cancel()
	compacted := make(chan error, 1)
	go func() {
		_, err := d.Compact(ctx, db.CompactOptions{PauseTimeout: time.Hour})
		compacted <- err
	}()
	select {
	case err := <-compacted:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation did not reopen the gate")
	}
	require.NoError(t, d.View(func(*bolt.Tx) error { return nil }))
}

func TestOpenDoesNotRemoveActiveCopy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	openTestDB(t, path)
	require.NoError(t, os.WriteFile(compactPath(path), []byte("active copy"), 0600))
	_, err := Open(path, 0600, &bolt.Options{Timeout: 20 * time.Millisecond})
	require.ErrorIs(t, err, errbolt.ErrTimeout)
	data, err := os.ReadFile(compactPath(path))
	require.NoError(t, err)
	require.Equal(t, "active copy", string(data))
}

func TestSafeOpenCleanupFailurePreservesDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	d := openTestDB(t, path)
	fillTestDB(t, d, 10)
	require.NoError(t, d.Close())
	require.NoError(t, os.Mkdir(compactPath(path), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(compactPath(path), "blocker"), nil, 0600))
	reopened, err := SafeOpen(path, 0600, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })
	checkTestDB(t, reopened, 0, 10, 10)
	backups, err := filepath.Glob(path + ".*.bak")
	require.NoError(t, err)
	require.Empty(t, backups)
}

func TestCompactReadOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	d := openTestDB(t, path)
	fillTestDB(t, d, 10)
	require.NoError(t, d.Close())
	require.NoError(t, os.WriteFile(compactPath(path), []byte("stale"), 0600))
	ro, err := Open(path, 0600, &bolt.Options{ReadOnly: true})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, ro.Close()) })
	res, err := ro.Compact(t.Context(), db.CompactOptions{})
	require.NoError(t, err)
	require.Equal(t, "read-only database", res.Reason)
	require.FileExists(t, compactPath(path))
	checkTestDB(t, ro, 0, 10, 10)
}

func TestCompactPreservesPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix file permissions")
	}
	path := filepath.Join(t.TempDir(), "test.db")
	d, err := Open(path, 0644, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, d.Close()) })
	fillTestDB(t, d, 100)
	require.NoError(t, os.Chmod(path, 0600))
	res, err := d.Compact(t.Context(), db.CompactOptions{})
	require.NoError(t, err)
	require.True(t, res.Compacted)
	fi, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), fi.Mode().Perm())
}

func TestCompactMultipleTransactions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	opts := &bolt.Options{PageSize: 8192}
	d, err := Open(path, 0600, opts)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, d.Close()) })
	opts.ReadOnly = true // Open must retain its own copy of the options.
	fillTestDB(t, d, 5000)
	deleteTestKeys(t, d, 0, 100)
	require.NoError(t, d.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucket([]byte("empty bucket"))
		if err != nil {
			return err
		}
		if err := b.SetSequence(99); err != nil {
			return err
		}
		return tx.Bucket([]byte(testBucket)).Put([]byte("empty value"), []byte{})
	}))
	res, err := d.Compact(t.Context(), db.CompactOptions{})
	require.NoError(t, err)
	require.True(t, res.Compacted)
	require.Equal(t, 8192, d.bdb.Info().PageSize)
	checkTestDB(t, d, 100, 5000, 5000)
	require.NoError(t, d.View(func(tx *bolt.Tx) error {
		require.Equal(t, uint64(99), tx.Bucket([]byte("empty bucket")).Sequence())
		require.NotNil(t, tx.Bucket([]byte(testBucket)).Get([]byte("empty value")))
		for err := range tx.Check() {
			require.NoError(t, err)
		}
		return nil
	}))
}

type cancelDuringCopy struct {
	context.Context
	remaining int
}

func (c *cancelDuringCopy) Err() error {
	c.remaining--
	if c.remaining <= 0 {
		return context.Canceled
	}
	return nil
}

func TestCompactCancellationPreservesDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	d := openTestDB(t, path)
	fillTestDB(t, d, 1000)
	ctx := &cancelDuringCopy{Context: context.WithoutCancel(t.Context()), remaining: 500}
	replaced, err := d.replaceWithCompacted(ctx)
	require.False(t, replaced)
	require.ErrorIs(t, err, context.Canceled)
	require.NoFileExists(t, compactPath(path))
	checkTestDB(t, d, 0, 1000, 1000)
	require.NoError(t, d.Update(func(*bolt.Tx) error { return nil }))
}

func TestCompactCrashRecovery(t *testing.T) {
	if phase := os.Getenv("BUILDKIT_TEST_COMPACT_PHASE"); phase != "" {
		path := os.Getenv("BUILDKIT_TEST_COMPACT_PATH")
		d := openTestDB(t, path)
		dst, err := d.writeCompactedCopy(t.Context(), compactPath(path))
		require.NoError(t, err)
		if phase == "replace" {
			replaced, err := d.installCompacted(dst)
			require.NoError(t, err)
			require.True(t, replaced)
		}
		os.Exit(0) // Simulate process exit without closing either database.
	}
	for _, phase := range []string{"copy", "replace"} {
		t.Run(phase, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "test.db")
			d := openTestDB(t, path)
			fillTestDB(t, d, 500)
			deleteTestKeys(t, d, 100, 500)
			require.NoError(t, d.Close())
			exe, err := os.Executable()
			require.NoError(t, err)
			cmd := exec.CommandContext(t.Context(), exe, "-test.run=^TestCompactCrashRecovery$")
			cmd.Env = append(os.Environ(), "BUILDKIT_TEST_COMPACT_PHASE="+phase, "BUILDKIT_TEST_COMPACT_PATH="+path)
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", out)
			reopened := openTestDB(t, path)
			checkTestDB(t, reopened, 0, 100, 500)
			require.NoFileExists(t, compactPath(path))
		})
	}
}

func TestCompactContainerdMetadata(t *testing.T) {
	root := t.TempDir()
	d := openTestDB(t, filepath.Join(root, "containerdmeta.db"))
	content, err := local.NewStore(filepath.Join(root, "content"))
	require.NoError(t, err)
	mdb := ctdmetadata.NewDB(d, content, nil)
	ctx := namespaces.WithNamespace(t.Context(), "buildkit")
	require.NoError(t, mdb.Init(ctx))
	lm := ctdmetadata.NewLeaseManager(mdb)
	lease, err := lm.Create(ctx, leases.WithID("preserved"))
	require.NoError(t, err)
	for range 2 {
		res, err := d.Compact(ctx, db.CompactOptions{})
		require.NoError(t, err)
		require.True(t, res.Compacted)
		got, err := lm.List(ctx)
		require.NoError(t, err)
		require.Equal(t, []leases.Lease{lease}, got)
		temporary, err := lm.Create(ctx, leases.WithID("temporary"))
		require.NoError(t, err)
		require.NoError(t, lm.Delete(ctx, temporary))
		_, err = mdb.GarbageCollect(ctx)
		require.NoError(t, err)
	}
	require.NoError(t, mdb.Close())
	require.ErrorIs(t, d.View(func(*bolt.Tx) error { return nil }), errbolt.ErrDatabaseNotOpen)
}

func BenchmarkView(b *testing.B) {
	for _, wrapped := range []bool{false, true} {
		b.Run(fmt.Sprintf("wrapped=%t", wrapped), func(b *testing.B) {
			path := filepath.Join(b.TempDir(), "test.db")
			var database db.DB
			var err error
			if wrapped {
				database, err = Open(path, 0600, nil)
			} else {
				database, err = bolt.Open(path, 0600, nil)
			}
			require.NoError(b, err)
			b.Cleanup(func() { require.NoError(b, database.Close()) })
			b.ReportAllocs()
			for b.Loop() {
				if err := database.View(func(*bolt.Tx) error { return nil }); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkCompact(b *testing.B) {
	for _, wrapped := range []bool{false, true} {
		b.Run(fmt.Sprintf("wrapped=%t", wrapped), func(b *testing.B) {
			d := openTestDB(b, filepath.Join(b.TempDir(), "test.db"))
			const total = 32768
			fillTestDB(b, d, total)
			deleteTestKeys(b, d, 0, total*3/4)
			b.SetBytes(total / 4 * testValSize)
			b.ReportAllocs()
			for b.Loop() {
				if wrapped {
					res, err := d.Compact(b.Context(), db.CompactOptions{})
					require.NoError(b, err)
					require.True(b, res.Compacted, res.Reason)
					continue
				}
				tmp := compactPath(d.path)
				dst, err := bolt.Open(tmp, 0600, &bolt.Options{NoSync: true, FreelistType: bolt.FreelistMapType})
				require.NoError(b, err)
				require.NoError(b, bolt.Compact(dst, d.bdb, compactTxMaxSize))
				require.NoError(b, dst.Sync())
				require.NoError(b, dst.Close())
				require.NoError(b, os.Remove(tmp))
			}
		})
	}
}
