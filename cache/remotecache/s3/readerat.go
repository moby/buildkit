package s3

import (
	"io"
	"sync"
)

type ReaderAtCloser interface {
	io.ReaderAt
	io.Closer
}

type readerAtCloser struct {
	mu     sync.Mutex
	offset int64
	rc     io.ReadCloser
	open   func(offset int64) (io.ReadCloser, error)
	closed bool
}

func toReaderAtCloser(open func(offset int64) (io.ReadCloser, error)) ReaderAtCloser {
	return &readerAtCloser{
		open: open,
	}
}

func (hrs *readerAtCloser) ReadAt(p []byte, off int64) (n int, err error) {
	hrs.mu.Lock()
	defer hrs.mu.Unlock()

	if hrs.closed {
		return 0, io.EOF
	}

	if len(p) == 0 {
		return 0, nil
	}

	// The underlying object is served as a stream, so a read at an offset other
	// than where the current stream sits requires reopening it with a new range.
	if hrs.rc == nil || off != hrs.offset {
		if hrs.rc != nil {
			hrs.rc.Close()
			hrs.rc = nil
		}
		rc, err := hrs.open(off)
		if err != nil {
			return 0, err
		}
		hrs.rc = rc
		hrs.offset = off
	}

	// io.ReadFull matches the io.ReaderAt contract: a read that returns fewer
	// bytes than requested must report a non-nil error.
	n, err = io.ReadFull(hrs.rc, p)
	hrs.offset = off + int64(n)
	return n, err
}

func (hrs *readerAtCloser) Close() error {
	hrs.mu.Lock()
	defer hrs.mu.Unlock()

	if hrs.closed {
		return nil
	}
	hrs.closed = true
	if hrs.rc != nil {
		return hrs.rc.Close()
	}

	return nil
}
