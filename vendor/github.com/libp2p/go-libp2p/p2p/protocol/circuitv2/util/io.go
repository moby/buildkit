package util

import (
	"errors"
	"io"

	pool "github.com/libp2p/go-buffer-pool"
	"github.com/libp2p/go-msgio/protoio"

	"github.com/gogo/protobuf/proto"
	"github.com/multiformats/go-varint"
)

type DelimitedReader struct {
	r   io.Reader
	buf []byte
}

// The gogo protobuf NewDelimitedReader is buffered, which may eat up stream data.
// So we need to implement a compatible delimited reader that reads unbuffered.
// There is a slowdown from unbuffered reading: when reading the message
// it can take multiple single byte Reads to read the length and another Read
// to read the message payload.
// However, this is not critical performance degradation as
//   - the reader is utilized to read one (dialer, stop) or two messages (hop) during
//     the handshake, so it's a drop in the water for the connection lifetime.
//   - messages are small (max 4k) and the length fits in a couple of bytes,
//     so overall we have at most three reads per message.
func NewDelimitedReader(r io.Reader, maxSize int) *DelimitedReader {
	return &DelimitedReader{r: r, buf: pool.Get(maxSize)}
}

func (d *DelimitedReader) Close() {
	if d.buf != nil {
		pool.Put(d.buf)
		d.buf = nil
	}
}

func (d *DelimitedReader) ReadByte() (byte, error) {
	buf := d.buf[:1]
	_, err := d.r.Read(buf)
	return buf[0], err
}

func (d *DelimitedReader) ReadMsg(msg proto.Message) error {
	mlen, err := varint.ReadUvarint(d)
	if err != nil {
		return err
	}

	if uint64(len(d.buf)) < mlen {
		return errors.New("message too large")
	}

	buf := d.buf[:mlen]
	_, err = io.ReadFull(d.r, buf)
	if err != nil {
		return err
	}

	return proto.Unmarshal(buf, msg)
}

func NewDelimitedWriter(w io.Writer) protoio.WriteCloser {
	return protoio.NewDelimitedWriter(w)
}
