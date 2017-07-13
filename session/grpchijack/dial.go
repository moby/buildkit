package grpchijack

import (
	"net"
	"strings"
	"sync"
	"time"

	controlapi "github.com/moby/buildkit/api/services/control"
	"github.com/moby/buildkit/session"
	"golang.org/x/net/context"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

var bufPool = sync.Pool{
	New: func() interface{} {
		return make([]byte, 32*1<<10)
	},
}

func Dialer(api controlapi.ControlClient) session.Dialer {
	return func(ctx context.Context, proto string, meta map[string][]string) (net.Conn, error) {

		meta = lowerHeaders(meta)

		md := metadata.MD(meta)

		ctx = metadata.NewContext(context.Background(), md)

		stream, err := api.Session(ctx)
		if err != nil {
			return nil, err
		}

		return streamToConn(stream), nil
	}
}

func streamToConn(stream grpc.Stream) net.Conn {
	return &conn{stream: stream, buf: make([]byte, 32*1<<10)}
}

type conn struct {
	stream  grpc.Stream
	buf     []byte
	lastBuf []byte
}

func (c *conn) Read(b []byte) (int, error) {
	if c.lastBuf != nil {
		n := copy(b, c.lastBuf)
		c.lastBuf = c.lastBuf[n:]
		if len(c.lastBuf) == 0 {
			c.lastBuf = nil
		}
		return n, nil
	}
	m := new(controlapi.BytesMessage)
	m.Data = c.buf

	if err := c.stream.RecvMsg(m); err != nil {
		return 0, err
	}
	c.buf = m.Data[:cap(m.Data)]

	n := copy(b, m.Data)
	if n < len(m.Data) {
		c.lastBuf = m.Data[n:]
	}

	return n, nil
}

func (c *conn) Write(b []byte) (int, error) {
	m := &controlapi.BytesMessage{Data: b}
	if err := c.stream.SendMsg(m); err != nil {
		return 0, err
	}
	return len(b), nil
}

func (s *conn) Close() error {
	if cs, ok := s.stream.(grpc.ClientStream); ok {
		return cs.CloseSend()
	}
	return nil
}

func (s *conn) LocalAddr() net.Addr {
	return dummyAddr{}
}
func (s *conn) RemoteAddr() net.Addr {
	return dummyAddr{}
}
func (s *conn) SetDeadline(t time.Time) error {
	return nil
}
func (s *conn) SetReadDeadline(t time.Time) error {
	return nil
}
func (s *conn) SetWriteDeadline(t time.Time) error {
	return nil
}

type dummyAddr struct {
}

func (d dummyAddr) Network() string {
	return "tcp"
}

func (d dummyAddr) String() string {
	return "localhost"
}

func lowerHeaders(in map[string][]string) map[string][]string {
	out := map[string][]string{}
	for k := range in {
		out[strings.ToLower(k)] = in[k]
	}
	return out
}
