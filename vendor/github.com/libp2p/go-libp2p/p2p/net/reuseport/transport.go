// Package reuseport provides a basic transport for automatically (and intelligently) reusing TCP ports.
//
// To use, construct a new Transport and configure listeners tr.Listen(...).
// When dialing (tr.Dial(...)), the transport will attempt to reuse the ports it's currently listening on,
// choosing the best one depending on the destination address.
//
// It is recommended to set SO_LINGER to 0 for all connections, otherwise
// reusing the port may fail when re-dialing a recently closed connection.
// See https://hea-www.harvard.edu/~fine/Tech/addrinuse.html for details.
package reuseport

import (
	"errors"
	"sync"

	logging "github.com/ipfs/go-log/v2"
)

var log = logging.Logger("reuseport-transport")

// ErrWrongProto is returned when dialing a protocol other than tcp.
var ErrWrongProto = errors.New("can only dial TCP over IPv4 or IPv6")

// Transport is a TCP reuse transport that reuses listener ports.
// The zero value is safe to use.
type Transport struct {
	v4 network
	v6 network
}

type network struct {
	mu        sync.RWMutex
	listeners map[*listener]struct{}
	dialer    *dialer
}
