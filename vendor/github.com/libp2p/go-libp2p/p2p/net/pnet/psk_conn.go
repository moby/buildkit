package pnet

import (
	"crypto/cipher"
	"crypto/rand"
	"io"
	"net"

	"github.com/libp2p/go-libp2p/core/pnet"

	"github.com/davidlazar/go-crypto/salsa20"
	pool "github.com/libp2p/go-buffer-pool"
)

// we are using buffer pool as user needs their slice back
// so we can't do XOR cripter in place
var (
	errShortNonce  = pnet.NewError("could not read full nonce")
	errInsecureNil = pnet.NewError("insecure is nil")
	errPSKNil      = pnet.NewError("pre-shread key is nil")
)

type pskConn struct {
	net.Conn
	psk *[32]byte

	writeS20 cipher.Stream
	readS20  cipher.Stream
}

func (c *pskConn) Read(out []byte) (int, error) {
	if c.readS20 == nil {
		nonce := make([]byte, 24)
		_, err := io.ReadFull(c.Conn, nonce)
		if err != nil {
			return 0, errShortNonce
		}
		c.readS20 = salsa20.New(c.psk, nonce)
	}

	n, err := c.Conn.Read(out) // read to in
	if n > 0 {
		c.readS20.XORKeyStream(out[:n], out[:n]) // decrypt to out buffer
	}
	return n, err
}

func (c *pskConn) Write(in []byte) (int, error) {
	if c.writeS20 == nil {
		nonce := make([]byte, 24)
		_, err := rand.Read(nonce)
		if err != nil {
			return 0, err
		}
		_, err = c.Conn.Write(nonce)
		if err != nil {
			return 0, err
		}

		c.writeS20 = salsa20.New(c.psk, nonce)
	}
	out := pool.Get(len(in))
	defer pool.Put(out)

	c.writeS20.XORKeyStream(out, in) // encrypt

	return c.Conn.Write(out) // send
}

var _ net.Conn = (*pskConn)(nil)

func newPSKConn(psk *[32]byte, insecure net.Conn) (net.Conn, error) {
	if insecure == nil {
		return nil, errInsecureNil
	}
	if psk == nil {
		return nil, errPSKNil
	}
	return &pskConn{
		Conn: insecure,
		psk:  psk,
	}, nil
}
