package pnet

import (
	"errors"
	"net"

	ipnet "github.com/libp2p/go-libp2p/core/pnet"
)

// NewProtectedConn creates a new protected connection
func NewProtectedConn(psk ipnet.PSK, conn net.Conn) (net.Conn, error) {
	if len(psk) != 32 {
		return nil, errors.New("expected 32 byte PSK")
	}
	var p [32]byte
	copy(p[:], psk)
	return newPSKConn(&p, conn)
}
