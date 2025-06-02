package sshprovider

import (
	"context"
	"net"
	"sync"
)

// pipeListener is a net.Listener that uses net.Pipe to create connections
// and is useful for in-memory uses of [newRawProvider].
//
// PipelListener can be created using the zero value.
type pipeListener struct {
	mu     sync.Mutex
	closed bool
	conns  []net.Conn

	chConn chan net.Conn

	closedCh chan struct{}
}

var _ net.Listener = (*pipeListener)(nil)

func (pl *pipeListener) Accept() (net.Conn, error) {
	pl.mu.Lock()
	if pl.chConn == nil {
		pl.chConn = make(chan net.Conn)
	}
	if pl.closedCh == nil {
		pl.closedCh = make(chan struct{})
	}
	pl.mu.Unlock()

	var conn net.Conn
	select {
	case <-pl.closedCh:
		return nil, net.ErrClosed
	case conn = <-pl.chConn:
	}

	if conn == nil {
		// channel is closed, no more connections can be accepted
		return nil, net.ErrClosed
	}

	pl.mu.Lock()
	defer pl.mu.Unlock()

	if pl.closed {
		conn.Close()
		return nil, net.ErrClosed
	}

	pl.conns = append(pl.conns, conn)

	return conn, nil
}

// Dialer implements the [DialerFn] type for [ProxyHandler].
func (pl *pipeListener) Dialer(ctx context.Context) (net.Conn, error) {
	pl.mu.Lock()
	if pl.closed {
		pl.mu.Unlock()
		return nil, net.ErrClosed
	}

	if pl.chConn == nil {
		pl.chConn = make(chan net.Conn)
	}

	pl.mu.Unlock()

	c1, c2 := net.Pipe()

	select {
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	case pl.chConn <- c2:
	}

	return c1, nil
}

func (pl *pipeListener) Addr() net.Addr {
	return &pipeAddr{}
}

type pipeAddr struct{}

func (pa *pipeAddr) Network() string {
	return "pipe"
}

func (pa *pipeAddr) String() string {
	return "pipe"
}

func (pl *pipeListener) Close() error {
	pl.mu.Lock()
	defer pl.mu.Unlock()

	if pl.closed {
		return nil
	}

	pl.closed = true
	if pl.chConn != nil {
		close(pl.chConn)
	}
	for _, conn := range pl.conns {
		conn.Close()
	}
	pl.conns = pl.conns[:0]
	return nil
}
