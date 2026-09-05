package s3

import (
	"io"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// chunkedReader serves data from an offset, returning at most chunk bytes per
// Read so that short reads can be exercised. An HTTP response body behaves this
// way, which is what the s3 reader is built on.
type chunkedReader struct {
	data   []byte
	pos    int
	chunk  int
	closed bool
}

func (r *chunkedReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n := len(p)
	if r.chunk > 0 && n > r.chunk {
		n = r.chunk
	}
	if n > len(r.data)-r.pos {
		n = len(r.data) - r.pos
	}
	copy(p, r.data[r.pos:r.pos+n])
	r.pos += n
	return n, nil
}

func (r *chunkedReader) Close() error {
	r.closed = true
	return nil
}

// opener records the offsets it was asked to open at, standing in for the
// ranged GetObject calls the real backend makes.
type opener struct {
	data    []byte
	chunk   int
	mu      sync.Mutex
	offsets []int64
	opened  []*chunkedReader
}

func (o *opener) open(offset int64) (io.ReadCloser, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.offsets = append(o.offsets, offset)
	rc := &chunkedReader{data: o.data[offset:], chunk: o.chunk}
	o.opened = append(o.opened, rc)
	return rc, nil
}

func (o *opener) openCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.offsets)
}

func testData(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i % 251)
	}
	return b
}

// TestReaderAtShortReads checks that a read is filled completely even when the
// underlying stream returns fewer bytes per Read than requested. Returning a
// short count with a nil error would violate the io.ReaderAt contract, and
// callers such as content.Copy rely on that contract.
//
// The chunk sizes matter. A stream handing back exactly half of what was asked
// for is the case that used to return 50 bytes and no error.
func TestReaderAtShortReads(t *testing.T) {
	for _, chunk := range []int{1, 7, 50, 99} {
		t.Run(strconv.Itoa(chunk), func(t *testing.T) {
			data := testData(1000)
			o := &opener{data: data, chunk: chunk}
			ra := toReaderAtCloser(o.open)
			defer ra.Close()

			p := make([]byte, 100)
			n, err := ra.ReadAt(p, 0)
			require.NoError(t, err)
			require.Equal(t, 100, n)
			require.Equal(t, data[:100], p)
		})
	}
}

// TestReaderAtSequentialReadsReuseStream checks that reads which continue where
// the previous one stopped do not reopen the object.
func TestReaderAtSequentialReadsReuseStream(t *testing.T) {
	data := testData(1000)
	o := &opener{data: data, chunk: 13}
	ra := toReaderAtCloser(o.open)
	defer ra.Close()

	var off int64
	for range 10 {
		p := make([]byte, 100)
		n, err := ra.ReadAt(p, off)
		require.NoError(t, err)
		require.Equal(t, 100, n)
		require.Equal(t, data[off:off+100], p)
		off += int64(n)
	}

	require.Equal(t, 1, o.openCount(), "sequential reads should reuse a single stream")
}

// TestReaderAtOffsetAccounting checks that the reader tracks its position from
// the offset it was asked for, not from wherever it happened to be. Getting
// this wrong caused redundant reopens and, when a later offset collided with
// the stale position, reads served from the wrong place in the object.
func TestReaderAtOffsetAccounting(t *testing.T) {
	data := testData(1000)
	o := &opener{data: data, chunk: 0}
	ra := toReaderAtCloser(o.open)
	defer ra.Close()

	// Start with a non-sequential read, which must reopen at that offset.
	p := make([]byte, 10)
	n, err := ra.ReadAt(p, 500)
	require.NoError(t, err)
	require.Equal(t, 10, n)
	require.Equal(t, data[500:510], p)
	require.Equal(t, []int64{500}, o.offsets)

	// Continuing from where that read ended must reuse the stream and return
	// the bytes that actually follow.
	p2 := make([]byte, 10)
	n, err = ra.ReadAt(p2, 510)
	require.NoError(t, err)
	require.Equal(t, 10, n)
	require.Equal(t, data[510:520], p2)
	require.Equal(t, 1, o.openCount(), "continuation read should not reopen")

	// A jump elsewhere must reopen at exactly that offset.
	p3 := make([]byte, 10)
	n, err = ra.ReadAt(p3, 100)
	require.NoError(t, err)
	require.Equal(t, 10, n)
	require.Equal(t, data[100:110], p3)
	require.Equal(t, []int64{500, 100}, o.offsets)
}

// TestReaderAtTruncatedObject checks that a read which cannot be satisfied
// reports an error rather than silently returning fewer bytes.
func TestReaderAtTruncatedObject(t *testing.T) {
	data := testData(50)
	o := &opener{data: data, chunk: 8}
	ra := toReaderAtCloser(o.open)
	defer ra.Close()

	p := make([]byte, 100)
	n, err := ra.ReadAt(p, 0)
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	require.Equal(t, 50, n)
}

// TestReaderAtEmptyRead checks that a zero length read is a no-op and does not
// open the object.
func TestReaderAtEmptyRead(t *testing.T) {
	o := &opener{data: testData(10)}
	ra := toReaderAtCloser(o.open)
	defer ra.Close()

	n, err := ra.ReadAt(nil, 0)
	require.NoError(t, err)
	require.Zero(t, n)
	require.Zero(t, o.openCount())
}

// TestReaderAtCloseReleasesStream checks that Close closes the open stream and
// that reads afterwards report EOF.
func TestReaderAtCloseReleasesStream(t *testing.T) {
	o := &opener{data: testData(100)}
	ra := toReaderAtCloser(o.open)

	p := make([]byte, 10)
	_, err := ra.ReadAt(p, 0)
	require.NoError(t, err)
	require.Len(t, o.opened, 1)
	require.False(t, o.opened[0].closed)

	require.NoError(t, ra.Close())
	require.True(t, o.opened[0].closed)

	n, err := ra.ReadAt(p, 0)
	require.ErrorIs(t, err, io.EOF)
	require.Zero(t, n)

	// Close is idempotent.
	require.NoError(t, ra.Close())
}

// TestReaderAtConcurrentReads checks that concurrent readers see correct data.
// Run with -race to catch unsynchronised access to the shared stream.
func TestReaderAtConcurrentReads(t *testing.T) {
	data := testData(4096)
	o := &opener{data: data, chunk: 11}
	ra := toReaderAtCloser(o.open)
	defer ra.Close()

	type read struct {
		off int64
		buf []byte
		n   int
		err error
	}
	reads := make([]read, 16)

	var wg sync.WaitGroup
	for i := range reads {
		wg.Add(1)
		go func() {
			defer wg.Done()
			off := int64(i * 64)
			p := make([]byte, 64)
			n, err := ra.ReadAt(p, off)
			reads[i] = read{off: off, buf: p, n: n, err: err}
		}()
	}
	wg.Wait()

	for _, r := range reads {
		require.NoError(t, r.err)
		require.Equal(t, 64, r.n)
		require.Equal(t, data[r.off:r.off+64], r.buf)
	}
}
