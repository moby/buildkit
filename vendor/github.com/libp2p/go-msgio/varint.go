package msgio

import (
	"encoding/binary"
	"io"
	"sync"

	pool "github.com/libp2p/go-buffer-pool"
	"github.com/multiformats/go-varint"
)

// varintWriter is the underlying type that implements the Writer interface.
type varintWriter struct {
	W io.Writer

	pool *pool.BufferPool
	lock sync.Mutex // for threadsafe writes
}

// NewVarintWriter wraps an io.Writer with a varint msgio framed writer.
// The msgio.Writer will write the length prefix of every message written
// as a varint, using https://golang.org/pkg/encoding/binary/#PutUvarint
func NewVarintWriter(w io.Writer) WriteCloser {
	return NewVarintWriterWithPool(w, pool.GlobalPool)
}

func NewVarintWriterWithPool(w io.Writer, p *pool.BufferPool) WriteCloser {
	return &varintWriter{
		pool: p,
		W:    w,
	}
}

func (s *varintWriter) Write(msg []byte) (int, error) {
	err := s.WriteMsg(msg)
	if err != nil {
		return 0, err
	}
	return len(msg), nil
}

func (s *varintWriter) WriteMsg(msg []byte) error {
	s.lock.Lock()
	defer s.lock.Unlock()

	buf := s.pool.Get(len(msg) + binary.MaxVarintLen64)
	n := binary.PutUvarint(buf, uint64(len(msg)))
	n += copy(buf[n:], msg)
	_, err := s.W.Write(buf[:n])
	s.pool.Put(buf)

	return err
}

func (s *varintWriter) Close() error {
	if c, ok := s.W.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

// varintReader is the underlying type that implements the Reader interface.
type varintReader struct {
	R  io.Reader
	br io.ByteReader // for reading varints.

	next int
	pool *pool.BufferPool
	lock sync.Mutex
	max  int // the maximal message size (in bytes) this reader handles
}

// NewVarintReader wraps an io.Reader with a varint msgio framed reader.
// The msgio.Reader will read whole messages at a time (using the length).
// Varints read according to https://golang.org/pkg/encoding/binary/#ReadUvarint
// Assumes an equivalent writer on the other side.
func NewVarintReader(r io.Reader) ReadCloser {
	return NewVarintReaderSize(r, defaultMaxSize)
}

// NewVarintReaderSize is equivalent to NewVarintReader but allows one to
// specify a max message size.
func NewVarintReaderSize(r io.Reader, maxMessageSize int) ReadCloser {
	return NewVarintReaderSizeWithPool(r, maxMessageSize, pool.GlobalPool)
}

// NewVarintReaderWithPool is the same as NewVarintReader but allows one to
// specify a buffer pool.
func NewVarintReaderWithPool(r io.Reader, p *pool.BufferPool) ReadCloser {
	return NewVarintReaderSizeWithPool(r, defaultMaxSize, p)
}

// NewVarintReaderWithPool is the same as NewVarintReader but allows one to
// specify a buffer pool and a max message size.
func NewVarintReaderSizeWithPool(r io.Reader, maxMessageSize int, p *pool.BufferPool) ReadCloser {
	if p == nil {
		panic("nil pool")
	}
	return &varintReader{
		R:    r,
		br:   &simpleByteReader{R: r},
		next: -1,
		pool: p,
		max:  maxMessageSize,
	}
}

// NextMsgLen reads the length of the next msg into s.lbuf, and returns it.
// WARNING: like Read, NextMsgLen is destructive. It reads from the internal
// reader.
func (s *varintReader) NextMsgLen() (int, error) {
	s.lock.Lock()
	defer s.lock.Unlock()
	return s.nextMsgLen()
}

func (s *varintReader) nextMsgLen() (int, error) {
	if s.next == -1 {
		length, err := varint.ReadUvarint(s.br)
		if err != nil {
			return 0, err
		}
		s.next = int(length)
	}
	return s.next, nil
}

func (s *varintReader) Read(msg []byte) (int, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	length, err := s.nextMsgLen()
	if err != nil {
		return 0, err
	}

	if length > len(msg) {
		return 0, io.ErrShortBuffer
	}
	_, err = io.ReadFull(s.R, msg[:length])
	s.next = -1 // signal we've consumed this msg
	return length, err
}

func (s *varintReader) ReadMsg() ([]byte, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	length, err := s.nextMsgLen()
	if err != nil {
		return nil, err
	}
	if length == 0 {
		s.next = -1
		return nil, nil
	}

	if length > s.max {
		return nil, ErrMsgTooLarge
	}

	msg := s.pool.Get(length)
	_, err = io.ReadFull(s.R, msg)
	s.next = -1 // signal we've consumed this msg
	return msg, err
}

func (s *varintReader) ReleaseMsg(msg []byte) {
	s.pool.Put(msg)
}

func (s *varintReader) Close() error {
	if c, ok := s.R.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

type simpleByteReader struct {
	R   io.Reader
	buf [1]byte
}

func (r *simpleByteReader) ReadByte() (c byte, err error) {
	if _, err := io.ReadFull(r.R, r.buf[:]); err != nil {
		return 0, err
	}
	return r.buf[0], nil
}
