//go:build !windows

package grpcclient

import (
	"context"
	"io"
	"net"
	"os"
	"time"

	"google.golang.org/grpc"
)

func getDialer() (string, grpc.DialOption) {
	dialFn := func(ctx context.Context, _ string) (net.Conn, error) {
		return stdioConn(), nil
	}
	return "localhost", grpc.WithContextDialer(dialFn)
}

func stdioConn() net.Conn {
	return &conn{
		Reader: os.Stdin,
		Writer: os.Stdout,
	}
}

type conn struct {
	io.Reader
	io.Writer
}

var _ net.Conn = &conn{}

func (c *conn) Close() error {
	if closer, ok := c.Writer.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

func (c *conn) LocalAddr() net.Addr {
	return dummyAddr{}
}

func (c *conn) RemoteAddr() net.Addr {
	return dummyAddr{}
}

func (c *conn) SetDeadline(_ time.Time) error {
	return nil
}

func (c *conn) SetReadDeadline(_ time.Time) error {
	return nil
}

func (c *conn) SetWriteDeadline(_ time.Time) error {
	return nil
}

type dummyAddr struct{}

var _ net.Addr = dummyAddr{}

func (d dummyAddr) Network() string {
	return "stdio"
}

func (d dummyAddr) String() string {
	return "localhost"
}
