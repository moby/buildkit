package client

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"time"

	"github.com/pkg/errors"
)

// DialHijack returns a hijacked connection with negotiated protocol proto.
func (cli *Client) DialHijack(ctx context.Context, url, proto string, meta map[string][]string) (net.Conn, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return nil, err
	}

	conn, _, err := cli.setupHijackConn(req, proto)
	return conn, err
}

func (cli *Client) setupHijackConn(req *http.Request, proto string) (_ net.Conn, _ string, retErr error) {
	ctx := req.Context()
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", proto)

	dialer := cli.Dialer()
	conn, err := dialer(ctx)
	if err != nil {
		return nil, "", errors.Wrap(err, "cannot connect to the Docker daemon. Is 'docker daemon' running on this host?")
	}
	defer func() {
		if retErr != nil {
			conn.Close()
		}
	}()

	// When we set up a TCP connection for hijack, there could be long periods
	// of inactivity (a long running command with no output) that in certain
	// network setups may cause ECONNTIMEOUT, leaving the client in an unknown
	// state. Setting TCP KeepAlive on the socket connection will prohibit
	// ECONNTIMEOUT unless the socket connection truly is broken
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		_ = tcpConn.SetKeepAlive(true)
		_ = tcpConn.SetKeepAlivePeriod(30 * time.Second)
	}

	hc := &hijackedConn{conn, bufio.NewReader(conn)}

	// Server hijacks the connection, error 'connection closed' expected
	resp, err := hc.RoundTrip(req)
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		_ = resp.Body.Close()
		return nil, "", errors.Errorf("unable to upgrade to %s, received %d", proto, resp.StatusCode)
	}

	if hc.r.Buffered() > 0 {
		// If there is buffered content, wrap the connection.  We return an
		// object that implements CloseWrite if the underlying connection
		// implements it.
		if _, ok := hc.Conn.(CloseWriter); ok {
			conn = &hijackedConnCloseWriter{hc}
		} else {
			conn = hc
		}
	} else {
		hc.r.Reset(nil)
	}

	mediaType := resp.Header.Get("Content-Type")
	return conn, mediaType, nil
}

// CloseWriter is an interface that implements structs
// that close input streams to prevent from writing.
type CloseWriter interface {
	CloseWrite() error
}

// hijackedConn wraps a net.Conn and is returned by setupHijackConn in the case
// that a) there was already buffered data in the http layer when Hijack() was
// called, and b) the underlying net.Conn does *not* implement CloseWrite().
// hijackedConn does not implement CloseWrite() either.
type hijackedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *hijackedConn) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := req.Write(c.Conn); err != nil {
		return nil, err
	}
	return http.ReadResponse(c.r, req)
}

func (c *hijackedConn) Read(b []byte) (int, error) {
	return c.r.Read(b)
}

// hijackedConnCloseWriter is a hijackedConn which additionally implements
// CloseWrite().  It is returned by setupHijackConn in the case that a) there
// was already buffered data in the http layer when Hijack() was called, and b)
// the underlying net.Conn *does* implement CloseWrite().
type hijackedConnCloseWriter struct {
	*hijackedConn
}

var _ CloseWriter = &hijackedConnCloseWriter{}

func (c *hijackedConnCloseWriter) CloseWrite() error {
	conn := c.Conn.(CloseWriter)
	return conn.CloseWrite()
}
