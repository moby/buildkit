// Package multistream implements a simple stream router for the
// multistream-select protocoli. The protocol is defined at
// https://github.com/multiformats/multistream-select
package multistream

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"runtime/debug"

	"io"
	"sync"

	"github.com/multiformats/go-varint"
)

// ErrTooLarge is an error to signal that an incoming message was too large
var ErrTooLarge = errors.New("incoming message was too large")

// ProtocolID identifies the multistream protocol itself and makes sure
// the multistream muxers on both sides of a channel can work with each other.
const ProtocolID = "/multistream/1.0.0"

var writerPool = sync.Pool{
	New: func() interface{} {
		return bufio.NewWriter(nil)
	},
}

// HandlerFunc is a user-provided function used by the MultistreamMuxer to
// handle a protocol/stream.
type HandlerFunc = func(protocol string, rwc io.ReadWriteCloser) error

// Handler is a wrapper to HandlerFunc which attaches a name (protocol) and a
// match function which can optionally be used to select a handler by other
// means than the name.
type Handler struct {
	MatchFunc func(string) bool
	Handle    HandlerFunc
	AddName   string
}

// MultistreamMuxer is a muxer for multistream. Depending on the stream
// protocol tag it will select the right handler and hand the stream off to it.
type MultistreamMuxer struct {
	handlerlock sync.RWMutex
	handlers    []Handler
}

// NewMultistreamMuxer creates a muxer.
func NewMultistreamMuxer() *MultistreamMuxer {
	return new(MultistreamMuxer)
}

// LazyConn is the connection type returned by the lazy negotiation functions.
type LazyConn interface {
	io.ReadWriteCloser
	// Flush flushes the lazy negotiation, if any.
	Flush() error
}

func writeUvarint(w io.Writer, i uint64) error {
	varintbuf := make([]byte, 16)
	n := varint.PutUvarint(varintbuf, i)
	_, err := w.Write(varintbuf[:n])
	if err != nil {
		return err
	}
	return nil
}

func delimWriteBuffered(w io.Writer, mes []byte) error {
	bw := getWriter(w)
	defer putWriter(bw)

	err := delimWrite(bw, mes)
	if err != nil {
		return err
	}

	return bw.Flush()
}

func delitmWriteAll(w io.Writer, messages ...[]byte) error {
	for _, mes := range messages {
		if err := delimWrite(w, mes); err != nil {
			return fmt.Errorf("failed to write messages %s, err: %v	", string(mes), err)
		}
	}

	return nil
}

func delimWrite(w io.Writer, mes []byte) error {
	err := writeUvarint(w, uint64(len(mes)+1))
	if err != nil {
		return err
	}

	_, err = w.Write(mes)
	if err != nil {
		return err
	}

	_, err = w.Write([]byte{'\n'})
	if err != nil {
		return err
	}
	return nil
}

func fulltextMatch(s string) func(string) bool {
	return func(a string) bool {
		return a == s
	}
}

// AddHandler attaches a new protocol handler to the muxer.
func (msm *MultistreamMuxer) AddHandler(protocol string, handler HandlerFunc) {
	msm.AddHandlerWithFunc(protocol, fulltextMatch(protocol), handler)
}

// AddHandlerWithFunc attaches a new protocol handler to the muxer with a match.
// If the match function returns true for a given protocol tag, the protocol
// will be selected even if the handler name and protocol tags are different.
func (msm *MultistreamMuxer) AddHandlerWithFunc(protocol string, match func(string) bool, handler HandlerFunc) {
	msm.handlerlock.Lock()
	defer msm.handlerlock.Unlock()

	msm.removeHandler(protocol)
	msm.handlers = append(msm.handlers, Handler{
		MatchFunc: match,
		Handle:    handler,
		AddName:   protocol,
	})
}

// RemoveHandler removes the handler with the given name from the muxer.
func (msm *MultistreamMuxer) RemoveHandler(protocol string) {
	msm.handlerlock.Lock()
	defer msm.handlerlock.Unlock()

	msm.removeHandler(protocol)
}

func (msm *MultistreamMuxer) removeHandler(protocol string) {
	for i, h := range msm.handlers {
		if h.AddName == protocol {
			msm.handlers = append(msm.handlers[:i], msm.handlers[i+1:]...)
			return
		}
	}
}

// Protocols returns the list of handler-names added to this this muxer.
func (msm *MultistreamMuxer) Protocols() []string {
	msm.handlerlock.RLock()
	defer msm.handlerlock.RUnlock()

	var out []string
	for _, h := range msm.handlers {
		out = append(out, h.AddName)
	}

	return out
}

// ErrIncorrectVersion is an error reported when the muxer protocol negotiation
// fails because of a ProtocolID mismatch.
var ErrIncorrectVersion = errors.New("client connected with incorrect version")

func (msm *MultistreamMuxer) findHandler(proto string) *Handler {
	msm.handlerlock.RLock()
	defer msm.handlerlock.RUnlock()

	for _, h := range msm.handlers {
		if h.MatchFunc(proto) {
			return &h
		}
	}

	return nil
}

// NegotiateLazy performs protocol selection and returns
// a multistream, the protocol used, the handler and an error. It is lazy
// because the write-handshake is performed on a subroutine, allowing this
// to return before that handshake is completed.
// Deprecated: use Negotiate instead.
func (msm *MultistreamMuxer) NegotiateLazy(rwc io.ReadWriteCloser) (rwc_ io.ReadWriteCloser, proto string, handler HandlerFunc, err error) {
	proto, handler, err = msm.Negotiate(rwc)
	return rwc, proto, handler, err
}

// Negotiate performs protocol selection and returns the protocol name and
// the matching handler function for it (or an error).
func (msm *MultistreamMuxer) Negotiate(rwc io.ReadWriteCloser) (proto string, handler HandlerFunc, err error) {
	defer func() {
		if rerr := recover(); rerr != nil {
			fmt.Fprintf(os.Stderr, "caught panic: %s\n%s\n", rerr, debug.Stack())
			err = fmt.Errorf("panic in multistream negotiation: %s", rerr)
		}
	}()

	// Send the multistream protocol ID
	// Ignore the error here.  We want the handshake to finish, even if the
	// other side has closed this rwc for writing. They may have sent us a
	// message and closed. Future writers will get an error anyways.
	_ = delimWriteBuffered(rwc, []byte(ProtocolID))

	line, err := ReadNextToken(rwc)
	if err != nil {
		return "", nil, err
	}

	if line != ProtocolID {
		rwc.Close()
		return "", nil, ErrIncorrectVersion
	}

loop:
	for {
		// Now read and respond to commands until they send a valid protocol id
		tok, err := ReadNextToken(rwc)
		if err != nil {
			return "", nil, err
		}

		h := msm.findHandler(tok)
		if h == nil {
			if err := delimWriteBuffered(rwc, []byte("na")); err != nil {
				return "", nil, err
			}
			continue loop
		}

		// Ignore the error here.  We want the handshake to finish, even if the
		// other side has closed this rwc for writing. They may have sent us a
		// message and closed. Future writers will get an error anyways.
		_ = delimWriteBuffered(rwc, []byte(tok))

		// hand off processing to the sub-protocol handler
		return tok, h.Handle, nil
	}

}

// Handle performs protocol negotiation on a ReadWriteCloser
// (i.e. a connection). It will find a matching handler for the
// incoming protocol and pass the ReadWriteCloser to it.
func (msm *MultistreamMuxer) Handle(rwc io.ReadWriteCloser) error {
	p, h, err := msm.Negotiate(rwc)
	if err != nil {
		return err
	}
	return h(p, rwc)
}

// ReadNextToken extracts a token from a Reader. It is used during
// protocol negotiation and returns a string.
func ReadNextToken(r io.Reader) (string, error) {
	tok, err := ReadNextTokenBytes(r)
	if err != nil {
		return "", err
	}

	return string(tok), nil
}

// ReadNextTokenBytes extracts a token from a Reader. It is used
// during protocol negotiation and returns a byte slice.
func ReadNextTokenBytes(r io.Reader) ([]byte, error) {
	data, err := lpReadBuf(r)
	switch err {
	case nil:
		return data, nil
	case ErrTooLarge:
		return nil, ErrTooLarge
	default:
		return nil, err
	}
}

func lpReadBuf(r io.Reader) ([]byte, error) {
	br, ok := r.(io.ByteReader)
	if !ok {
		br = &byteReader{r}
	}

	length, err := varint.ReadUvarint(br)
	if err != nil {
		return nil, err
	}

	if length > 1024 {
		return nil, ErrTooLarge
	}

	buf := make([]byte, length)
	_, err = io.ReadFull(r, buf)
	if err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return nil, err
	}

	if len(buf) == 0 || buf[length-1] != '\n' {
		return nil, errors.New("message did not have trailing newline")
	}

	// slice off the trailing newline
	buf = buf[:length-1]

	return buf, nil

}

// byteReader implements the ByteReader interface that ReadUVarint requires
type byteReader struct {
	io.Reader
}

func (br *byteReader) ReadByte() (byte, error) {
	var b [1]byte
	n, err := br.Read(b[:])
	if n == 1 {
		return b[0], nil
	}
	if err == nil {
		if n != 0 {
			panic("read more bytes than buffer size")
		}
		err = io.ErrNoProgress
	}
	return 0, err
}

func getWriter(w io.Writer) *bufio.Writer {
	bw := writerPool.Get().(*bufio.Writer)
	bw.Reset(w)
	return bw
}

func putWriter(bw *bufio.Writer) {
	bw.Reset(nil)
	writerPool.Put(bw)
}
