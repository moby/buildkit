package reuseport

import (
	"context"

	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

// Dial dials the given multiaddr, reusing ports we're currently listening on if
// possible.
//
// Dial attempts to be smart about choosing the source port. For example, If
// we're dialing a loopback address and we're listening on one or more loopback
// ports, Dial will randomly choose one of the loopback ports and addresses and
// reuse it.
func (t *Transport) Dial(raddr ma.Multiaddr) (manet.Conn, error) {
	return t.DialContext(context.Background(), raddr)
}

// DialContext is like Dial but takes a context.
func (t *Transport) DialContext(ctx context.Context, raddr ma.Multiaddr) (manet.Conn, error) {
	network, addr, err := manet.DialArgs(raddr)
	if err != nil {
		return nil, err
	}
	var d *dialer
	switch network {
	case "tcp4":
		d = t.v4.getDialer(network)
	case "tcp6":
		d = t.v6.getDialer(network)
	default:
		return nil, ErrWrongProto
	}
	conn, err := d.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	maconn, err := manet.WrapNetConn(conn)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return maconn, nil
}

func (n *network) getDialer(network string) *dialer {
	n.mu.RLock()
	d := n.dialer
	n.mu.RUnlock()
	if d == nil {
		n.mu.Lock()
		defer n.mu.Unlock()

		if n.dialer == nil {
			n.dialer = newDialer(n.listeners)
		}
		d = n.dialer
	}
	return d
}
