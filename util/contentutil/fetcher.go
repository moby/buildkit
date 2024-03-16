package contentutil

import (
	"context"
	"io"

	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/remotes"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
)

func FromFetcher(f remotes.Fetcher) content.Provider {
	return &fetchedProvider{
		f: f,
	}
}

type fetchedProvider struct {
	f remotes.Fetcher
}

func (p *fetchedProvider) ReaderAt(ctx context.Context, desc ocispecs.Descriptor) (content.ReaderAt, error) {
	open := func() (io.Reader, io.Closer, int64, error) {
		rc, err := p.f.Fetch(ctx, desc)
		return rc, rc, desc.Size, err
	}
	reader := &readerAt{open: open}
	if err := reader.reopen(); err != nil {
		return nil, err
	}
	return reader, nil
}

type readerAt struct {
	reader io.Reader
	closer io.Closer
	open   func() (io.Reader, io.Closer, int64, error)
	size   int64
	offset int64
}

func (r *readerAt) reopen() error {
	reader, closer, size, err := r.open()
	if err != nil {
		return err
	}
	r.reader = reader
	r.closer = closer
	r.size = size
	r.offset = 0
	return nil
}

func (r *readerAt) seek(off int64) error {
	if r.offset > off {
		if err := r.closer.Close(); err != nil {
			return err
		}
		if err := r.reopen(); err != nil {
			return err
		}
	}
	var buffer [1024]byte
	for r.offset < off {
		var toRead = off - r.offset
		if toRead > int64(len(buffer)) {
			toRead = int64(len(buffer))
		}
		n, err := r.reader.Read(buffer[0:toRead])
		if err != nil {
			return err
		}
		r.offset += int64(n)
	}
	return nil
}

func (r *readerAt) ReadAt(b []byte, off int64) (int, error) {
	if r.reader == nil {
		if err := r.reopen(); err != nil {
			return 0, err
		}
	}

	if ra, ok := r.reader.(io.ReaderAt); ok {
		return ra.ReadAt(b, off)
	}

	if r.offset != off {
		if seeker, ok := r.reader.(io.Seeker); ok {
			if _, err := seeker.Seek(off, io.SeekStart); err != nil {
				return 0, err
			}
			r.offset = off
		} else {
			err := r.seek(off)
			if err != nil {
				return 0, err
			}
		}
	}

	var totalN int
	for len(b) > 0 {
		n, err := r.reader.Read(b)
		if err == io.EOF && n == len(b) {
			err = nil
		}
		r.offset += int64(n)
		totalN += n
		b = b[n:]
		if err != nil {
			return totalN, err
		}
	}
	return totalN, nil
}

func (r *readerAt) Size() int64 {
	return r.size
}

func (r *readerAt) Close() error {
	return r.closer.Close()
}
