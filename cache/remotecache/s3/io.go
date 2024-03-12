package s3

import (
	"io"
	"os"

	"github.com/containerd/containerd/content"
)

func ReadSeekerFromReaderAt(ra content.ReaderAt) io.ReadSeeker {
	return &readerAtSeeker{
		ReaderAt: ra,
	}
}

type readerAtSeeker struct {
	pos int64
	content.ReaderAt
}

var _ io.ReadSeeker = &readerAtSeeker{}

// Read according to offset position
// Read reads up to len(p) bytes into p. It returns the number of bytes
// read (0 <= n <= len(p)) and any error encountered.
func (r *readerAtSeeker) Read(p []byte) (n int, err error) {
	// Delegate to ReadAt, using current position
	n, err = r.ReadAt(p, r.pos)
	if err == nil {
		// Move the position forward
		r.pos += int64(n)
	}
	return n, err
}

// Seek sets the offset for the next Read, interpreted according to whence:
// io.SeekStart means relative to the origin of the file, io.SeekCurrent means
// relative to the current offset, and io.SeekEnd means relative to the EOF.
func (r *readerAtSeeker) Seek(offset int64, whence int) (int64, error) {
	var newPos int64

	switch whence {
	case io.SeekStart:
		newPos = offset
	case io.SeekCurrent:
		newPos = r.pos + offset
	case io.SeekEnd:
		newPos = r.Size() + offset
	default:
		return 0, os.ErrInvalid
	}

	if newPos < 0 || newPos > r.Size() {
		return 0, os.ErrInvalid
	}

	r.pos = newPos
	return r.pos, nil
}
