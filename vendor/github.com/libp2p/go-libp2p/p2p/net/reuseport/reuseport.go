package reuseport

import (
	"context"
	"net"

	"github.com/libp2p/go-reuseport"
)

var fallbackDialer net.Dialer

// Dials using reuseport and then redials normally if that fails.
func reuseDial(ctx context.Context, laddr *net.TCPAddr, network, raddr string) (con net.Conn, err error) {
	if laddr == nil {
		return fallbackDialer.DialContext(ctx, network, raddr)
	}

	d := net.Dialer{
		LocalAddr: laddr,
		Control:   reuseport.Control,
	}

	con, err = d.DialContext(ctx, network, raddr)
	if err == nil {
		return con, nil
	}

	if reuseErrShouldRetry(err) && ctx.Err() == nil {
		// We could have an existing socket open or we could have one
		// stuck in TIME-WAIT.
		log.Debugf("failed to reuse port, will try again with a random port: %s", err)
		con, err = fallbackDialer.DialContext(ctx, network, raddr)
	}
	return con, err
}
