package s3

import (
	"bytes"
	"fmt"
	"io"
	"testing"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
)

// fakeObject mimics S3 GetObject: offset 0 is a plain GET, any other offset is
// "Range: bytes=<off>-", which S3 answers with 416 InvalidRange once off is at
// or past the end of the object.
type fakeObject struct {
	data  []byte
	opens []int64
}

func (o *fakeObject) open(off int64) (io.ReadCloser, error) {
	o.opens = append(o.opens, off)
	if off > 0 && off >= int64(len(o.data)) {
		return nil, errors.Errorf("StatusCode: 416, api error InvalidRange: bytes=%d-", off)
	}
	return io.NopCloser(bytes.NewReader(o.data[off:])), nil
}

// readFrom reads the way contentutil's localFetcher does when containerd's
// content.Copy resumes a partial ingest: seek to start, then call ReadAt with
// the running offset until io.EOF, never bounding the offset by size.
func readFrom(ra io.ReaderAt, start int64, bufSize int) ([]byte, error) {
	var out bytes.Buffer
	buf := make([]byte, bufSize)
	off := start
	for {
		n, err := ra.ReadAt(buf, off)
		out.Write(buf[:n])
		off += int64(n)
		if errors.Is(err, io.EOF) {
			if n > 0 {
				continue
			}
			return out.Bytes(), nil
		}
		if err != nil {
			return out.Bytes(), err
		}
	}
}

func newTestReaderAt(obj *fakeObject) *readerAt {
	return &readerAt{ReaderAtCloser: toReaderAtCloser(obj.open), size: int64(len(obj.data))}
}

func TestReaderAtResumeFromOffset(t *testing.T) {
	data := bytes.Repeat([]byte("0123456789abcdef"), 4096) // 64KiB
	for _, start := range []int64{0, 1, 1000, int64(len(data)) / 2, int64(len(data)) - 1} {
		t.Run(fmt.Sprintf("start=%d", start), func(t *testing.T) {
			obj := &fakeObject{data: data}
			got, err := readFrom(newTestReaderAt(obj), start, 4096)
			require.NoError(t, err)
			require.Equal(t, data[start:], got)
			// A sequential read from any offset needs exactly one GetObject.
			require.Equal(t, []int64{start}, obj.opens)
		})
	}
}

func TestReaderAtAtEndOfBlob(t *testing.T) {
	// Resuming an ingest that was already fully written seeks straight to size.
	obj := &fakeObject{data: []byte("hello")}
	n, err := newTestReaderAt(obj).ReadAt(make([]byte, 8), 5)
	require.Equal(t, 0, n)
	require.ErrorIs(t, err, io.EOF)
	require.Empty(t, obj.opens)
}
