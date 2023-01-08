package noise

import (
	"encoding/binary"
	"io"

	pool "github.com/libp2p/go-buffer-pool"
	"golang.org/x/crypto/chacha20poly1305"
)

// MaxTransportMsgLength is the Noise-imposed maximum transport message length,
// inclusive of the MAC size (16 bytes, Poly1305 for noise-libp2p).
const MaxTransportMsgLength = 0xffff

// MaxPlaintextLength is the maximum payload size. It is MaxTransportMsgLength
// minus the MAC size. Payloads over this size will be automatically chunked.
const MaxPlaintextLength = MaxTransportMsgLength - chacha20poly1305.Overhead

// LengthPrefixLength is the length of the length prefix itself, which precedes
// all transport messages in order to delimit them. In bytes.
const LengthPrefixLength = 2

// Read reads from the secure connection, returning plaintext data in `buf`.
//
// Honours io.Reader in terms of behaviour.
func (s *secureSession) Read(buf []byte) (int, error) {
	s.readLock.Lock()
	defer s.readLock.Unlock()

	// 1. If we have queued received bytes:
	//   1a. If len(buf) < len(queued), saturate buf, update seek pointer, return.
	//   1b. If len(buf) >= len(queued), copy remaining to buf, release queued buffer back into pool, return.
	//
	// 2. Else, read the next message off the wire; next_len is length prefix.
	//   2a. If len(buf) >= next_len, copy the message to input buffer (zero-alloc path), and return.
	//   2b. If len(buf) >= (next_len - length of Authentication Tag), get buffer from pool, read encrypted message into it.
	//       decrypt message directly into the input buffer and return the buffer obtained from the pool.
	//   2c. If len(buf) < next_len, obtain buffer from pool, copy entire message into it, saturate buf, update seek pointer.
	if s.qbuf != nil {
		// we have queued bytes; copy as much as we can.
		copied := copy(buf, s.qbuf[s.qseek:])
		s.qseek += copied
		if s.qseek == len(s.qbuf) {
			// queued buffer is now empty, reset and release.
			pool.Put(s.qbuf)
			s.qseek, s.qbuf = 0, nil
		}
		return copied, nil
	}

	// length of the next encrypted message.
	nextMsgLen, err := s.readNextInsecureMsgLen()
	if err != nil {
		return 0, err
	}

	// If the buffer is atleast as big as the encrypted message size,
	// we can read AND decrypt in place.
	if len(buf) >= nextMsgLen {
		if err := s.readNextMsgInsecure(buf[:nextMsgLen]); err != nil {
			return 0, err
		}

		dbuf, err := s.decrypt(buf[:0], buf[:nextMsgLen])
		if err != nil {
			return 0, err
		}

		return len(dbuf), nil
	}

	// otherwise, we get a buffer from the pool so we can read the message into it
	// and then decrypt in place, since we're retaining the buffer (or a view thereof).
	cbuf := pool.Get(nextMsgLen)
	if err := s.readNextMsgInsecure(cbuf); err != nil {
		return 0, err
	}

	if s.qbuf, err = s.decrypt(cbuf[:0], cbuf); err != nil {
		return 0, err
	}

	// copy as many bytes as we can; update seek pointer.
	s.qseek = copy(buf, s.qbuf)

	return s.qseek, nil
}

// Write encrypts the plaintext `in` data and sends it on the
// secure connection.
func (s *secureSession) Write(data []byte) (int, error) {
	s.writeLock.Lock()
	defer s.writeLock.Unlock()

	var (
		written int
		cbuf    []byte
		total   = len(data)
	)

	if total < MaxPlaintextLength {
		cbuf = pool.Get(total + chacha20poly1305.Overhead + LengthPrefixLength)
	} else {
		cbuf = pool.Get(MaxTransportMsgLength + LengthPrefixLength)
	}

	defer pool.Put(cbuf)

	for written < total {
		end := written + MaxPlaintextLength
		if end > total {
			end = total
		}

		b, err := s.encrypt(cbuf[:LengthPrefixLength], data[written:end])
		if err != nil {
			return 0, err
		}

		binary.BigEndian.PutUint16(b, uint16(len(b)-LengthPrefixLength))

		_, err = s.writeMsgInsecure(b)
		if err != nil {
			return written, err
		}
		written = end
	}
	return written, nil
}

// readNextInsecureMsgLen reads the length of the next message on the insecureConn channel.
func (s *secureSession) readNextInsecureMsgLen() (int, error) {
	_, err := io.ReadFull(s.insecureReader, s.rlen[:])
	if err != nil {
		return 0, err
	}

	return int(binary.BigEndian.Uint16(s.rlen[:])), err
}

// readNextMsgInsecure tries to read exactly len(buf) bytes into buf from
// the insecureConn channel and returns the error, if any.
// Ideally, for reading a message, you'd first want to call `readNextInsecureMsgLen`
// to determine the size of the next message to be read from the insecureConn channel and then call
// this function with a buffer of exactly that size.
func (s *secureSession) readNextMsgInsecure(buf []byte) error {
	_, err := io.ReadFull(s.insecureReader, buf)
	return err
}

// writeMsgInsecure writes to the insecureConn conn.
// data will be prefixed with its length in bytes, written as a 16-bit uint in network order.
func (s *secureSession) writeMsgInsecure(data []byte) (int, error) {
	return s.insecureConn.Write(data)
}
