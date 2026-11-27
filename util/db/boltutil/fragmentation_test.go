package boltutil

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	bolt "go.etcd.io/bbolt"
)

func TestFragmentationStats(t *testing.T) {
	for _, pageSize := range []int{4096, 16384, 65536} {
		t.Run(fmt.Sprint(pageSize), func(t *testing.T) {
			d, err := Open(filepath.Join(t.TempDir(), "test.db"), 0600, &bolt.Options{PageSize: pageSize})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, d.Close()) })
			require.NoError(t, d.Update(func(tx *bolt.Tx) error {
				root, err := tx.CreateBucket([]byte("root"))
				require.NoError(t, err)
				_, err = root.CreateBucket([]byte("empty"))
				require.NoError(t, err)
				inline, err := root.CreateBucket([]byte("inline"))
				require.NoError(t, err)
				require.NoError(t, inline.Put([]byte("key"), []byte("value")))
				nested, err := root.CreateBucket([]byte("nested"))
				require.NoError(t, err)
				nested.FillPercent = 0.3
				for i := range 6000 {
					require.NoError(t, nested.Put(testKey(i), bytes.Repeat([]byte("x"), 128)))
				}
				large, err := root.CreateBucket([]byte("large"))
				require.NoError(t, err)
				require.NoError(t, large.Put([]byte("value"), make([]byte, 100*pageSize)))
				require.NoError(t, large.Put([]byte("small"), []byte("value")))
				return nil
			}))
			require.NoError(t, d.View(func(tx *bolt.Tx) error {
				b := tx.Bucket([]byte("root"))
				s := b.Stats()
				require.Positive(t, s.InlineBucketN)
				require.Positive(t, s.BranchPageN)
				require.Positive(t, s.LeafOverflowN)
				large := b.Bucket([]byte("large")).Stats()
				require.Equal(t, 1, large.LeafPageN)
				overflowSlack := large.LeafAlloc - large.LeafInuse
				expected := int64(max(s.LeafAlloc-s.LeafInuse-overflowSlack-(s.BucketN-s.InlineBucketN)*pageSize, 0))
				require.Positive(t, expected)
				f, err := os.Open(d.path)
				require.NoError(t, err)
				defer f.Close()
				got, err := fragmentedBytes(t.Context(), f, int64(pageSize), tx.Size(), []uint64{uint64(b.Root())})
				require.NoError(t, err)
				require.Equal(t, expected, got)
				return nil
			}))
		})
	}
}

func TestFragmentationCancellation(t *testing.T) {
	d := openTestDB(t, filepath.Join(t.TempDir(), "test.db"))
	fillTestDB(t, d, 100)
	require.NoError(t, d.View(func(tx *bolt.Tx) error {
		f, err := os.Open(d.path)
		require.NoError(t, err)
		defer f.Close()
		root := uint64(tx.Bucket([]byte(testBucket)).Root())
		for _, after := range []int{1, 2, 3, 10} {
			ctx, cancel := context.WithCancelCause(t.Context())
			cause := errors.New("writer arrived")
			r := &fragmentationCancelReader{ReaderAt: f, after: after, cancel: func() { cancel(cause) }}
			got, err := fragmentedBytes(ctx, r, int64(d.pageSize), tx.Size(), []uint64{root})
			cancel(context.Canceled)
			require.ErrorIs(t, err, cause)
			require.Zero(t, got)
			require.Equal(t, after, r.reads)
		}
		return nil
	}))
	checkTestDB(t, d, 0, 100, 100)
	require.NoError(t, d.Update(func(*bolt.Tx) error { return nil }))
}

type fragmentationCancelReader struct {
	io.ReaderAt
	after  int
	reads  int
	cancel func()
}

func (r *fragmentationCancelReader) ReadAt(p []byte, off int64) (int, error) {
	r.reads++
	if r.reads == r.after {
		r.cancel()
	}
	return r.ReaderAt.ReadAt(p, off)
}

func TestFragmentationInvalidPages(t *testing.T) {
	const pageSize = 4096
	for _, tc := range []struct {
		name   string
		mutate func([]byte)
	}{
		{
			name: "identity",
			mutate: func(p []byte) {
				binary.NativeEndian.PutUint64(p, 3)
			},
		},
		{
			name: "flags",
			mutate: func(p []byte) {
				binary.NativeEndian.PutUint16(p[8:], 4)
			},
		},
		{
			name: "overflow",
			mutate: func(p []byte) {
				binary.NativeEndian.PutUint32(p[12:], ^uint32(0))
			},
		},
		{
			name: "element count",
			mutate: func(p []byte) {
				binary.NativeEndian.PutUint16(p[10:], ^uint16(0))
			},
		},
		{
			name: "element offset",
			mutate: func(p []byte) {
				binary.NativeEndian.PutUint32(p[20:], ^uint32(0))
			},
		},
		{
			name: "short bucket header",
			mutate: func(p []byte) {
				binary.NativeEndian.PutUint32(p[16:], 1)
			},
		},
		{
			name: "branch cycle",
			mutate: func(p []byte) {
				binary.NativeEndian.PutUint16(p[8:], 1)
				binary.NativeEndian.PutUint64(p[24:], 2)
			},
		},
		{
			name: "branch out of bounds",
			mutate: func(p []byte) {
				binary.NativeEndian.PutUint16(p[8:], 1)
				binary.NativeEndian.PutUint64(p[24:], ^uint64(0))
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := make([]byte, 3*pageSize)
			p := data[2*pageSize:]
			binary.NativeEndian.PutUint64(p, 2)
			binary.NativeEndian.PutUint16(p[8:], 2)
			binary.NativeEndian.PutUint16(p[10:], 1)
			binary.NativeEndian.PutUint32(p[20:], 16)
			binary.NativeEndian.PutUint32(p[24:], 1)
			binary.NativeEndian.PutUint32(p[28:], 1)
			tc.mutate(p)
			got, err := fragmentedBytes(t.Context(), bytes.NewReader(data), pageSize, int64(len(data)), []uint64{2})
			require.Error(t, err)
			require.Zero(t, got)
		})
	}
	t.Run("truncated", func(t *testing.T) {
		got, err := fragmentedBytes(t.Context(), bytes.NewReader(nil), pageSize, 3*pageSize, []uint64{2})
		require.ErrorIs(t, err, io.EOF)
		require.Zero(t, got)
	})
}

func FuzzFragmentedBytes(f *testing.F) {
	const pageSize = 4096
	f.Add([]byte{})
	leaf := make([]byte, pageSize)
	binary.NativeEndian.PutUint64(leaf, 2)
	binary.NativeEndian.PutUint16(leaf[8:], 2)
	f.Add(leaf)
	// Seed child traversal as well as malformed headers.
	child := bytes.Clone(leaf)
	binary.NativeEndian.PutUint64(child, 3)
	binary.NativeEndian.PutUint16(child[10:], 1)
	binary.NativeEndian.PutUint32(child[20:], 16)
	binary.NativeEndian.PutUint32(child[24:], 1)
	binary.NativeEndian.PutUint32(child[28:], 1)
	copy(child[32:], "kv")
	branch := bytes.Clone(leaf)
	binary.NativeEndian.PutUint16(branch[8:], 1)
	binary.NativeEndian.PutUint16(branch[10:], 1)
	binary.NativeEndian.PutUint32(branch[16:], 16)
	binary.NativeEndian.PutUint32(branch[20:], 1)
	binary.NativeEndian.PutUint64(branch[24:], 3)
	branch[32] = 'k'
	f.Add(append(branch, child...))
	nested := bytes.Clone(leaf)
	binary.NativeEndian.PutUint16(nested[10:], 1)
	binary.NativeEndian.PutUint32(nested[16:], 1)
	binary.NativeEndian.PutUint32(nested[20:], 16)
	binary.NativeEndian.PutUint32(nested[24:], 1)
	binary.NativeEndian.PutUint32(nested[28:], 16)
	nested[32] = 'b'
	binary.NativeEndian.PutUint64(nested[33:], 3)
	f.Add(append(nested, child...))
	f.Fuzz(func(t *testing.T, pages []byte) {
		if len(pages) > 1<<20 {
			t.Skip()
		}
		data := append(make([]byte, 2*pageSize), pages...)
		got, err := fragmentedBytes(t.Context(), bytes.NewReader(data), pageSize, int64(len(data)), []uint64{2})
		if err != nil {
			require.Zero(t, got)
		} else {
			require.GreaterOrEqual(t, got, int64(0))
			require.LessOrEqual(t, got, int64(len(data)))
		}
	})
}
