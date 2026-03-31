package session

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/moby/buildkit/session/testutil"
	"github.com/stretchr/testify/require"
)

type blockingWriteConn struct {
	writeStarted chan struct{}
	releaseWrite chan struct{}
}

type testAddr struct{}

func (c *blockingWriteConn) Read(_ []byte) (int, error) {
	return 0, io.EOF
}

func (c *blockingWriteConn) Write(p []byte) (int, error) {
	select {
	case <-c.writeStarted:
	default:
		close(c.writeStarted)
	}
	<-c.releaseWrite
	return len(p), nil
}

func (c *blockingWriteConn) Close() error {
	return nil
}

func (c *blockingWriteConn) LocalAddr() net.Addr {
	return testAddr{}
}

func (c *blockingWriteConn) RemoteAddr() net.Addr {
	return testAddr{}
}

func (c *blockingWriteConn) SetDeadline(_ time.Time) error {
	return nil
}

func (c *blockingWriteConn) SetReadDeadline(_ time.Time) error {
	return nil
}

func (c *blockingWriteConn) SetWriteDeadline(_ time.Time) error {
	return nil
}

func (testAddr) Network() string {
	return "tcp"
}

func (testAddr) String() string {
	return "localhost"
}

type hijackOnlyResponseWriter struct {
	conn net.Conn
	hdr  http.Header
}

func (w *hijackOnlyResponseWriter) Header() http.Header {
	if w.hdr == nil {
		w.hdr = make(http.Header)
	}
	return w.hdr
}

func (w *hijackOnlyResponseWriter) Write(p []byte) (int, error) {
	return len(p), nil
}

func (w *hijackOnlyResponseWriter) WriteHeader(_ int) {}

func (w *hijackOnlyResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	rw := bufio.NewReadWriter(bufio.NewReader(w.conn), bufio.NewWriter(w.conn))
	return w.conn, rw, nil
}

func startSessionForTest(t *testing.T, sm *Manager, id, sharedKey string) func() {
	t.Helper()

	s, err := NewSession(t.Context(), sharedKey)
	require.NoError(t, err)
	if id != "" {
		s.id = id
	}

	runCtx, runCancel := context.WithCancelCause(t.Context())
	runErrCh := make(chan error, 1)
	dialer := Dialer(testutil.TestStream(testutil.Handler(sm.HandleConn)))

	go func() {
		runErrCh <- s.Run(runCtx, dialer)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeoutCause(t.Context(), 50*time.Millisecond, context.DeadlineExceeded)
		c, err := sm.Get(ctx, s.ID(), true)
		cancel()
		if err == nil && c != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	ctx, cancel := context.WithTimeoutCause(t.Context(), 50*time.Millisecond, context.DeadlineExceeded)
	c, err := sm.Get(ctx, s.ID(), true)
	cancel()
	require.NoError(t, err)
	require.NotNil(t, c, "session did not become active")

	return func() {
		require.NoError(t, s.Close())
		runCancel(context.Canceled)
		select {
		case err := <-runErrCh:
			require.NoError(t, err)
		case <-time.After(2 * time.Second):
			t.Fatalf("session run did not exit in time for %s", s.ID())
		}
	}
}

func TestManagerHandleHTTPRequestDoesNotBlockUnrelatedGet(t *testing.T) {
	sm, err := NewManager()
	require.NoError(t, err)

	const activeID = "existing-active"
	cleanup := startSessionForTest(t, sm, activeID, "existing-key")
	defer cleanup()

	conn := &blockingWriteConn{
		writeStarted: make(chan struct{}),
		releaseWrite: make(chan struct{}),
	}
	reqCtx, reqCancel := context.WithTimeoutCause(t.Context(), 250*time.Millisecond, context.DeadlineExceeded)
	defer reqCancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, "http://session.local/upgrade", nil)
	require.NoError(t, err)
	req.Header.Set(headerSessionID, "other-http-session")
	req.Header.Set("Upgrade", "h2c")

	rw := &hijackOnlyResponseWriter{conn: conn}
	httpErrCh := make(chan error, 1)
	go func() {
		httpErrCh <- sm.HandleHTTPRequest(reqCtx, rw, req)
	}()

	select {
	case <-conn.writeStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for HandleHTTPRequest response write")
	}

	getCtx, getCancel := context.WithTimeoutCause(t.Context(), 100*time.Millisecond, context.DeadlineExceeded)
	defer getCancel()

	getDone := make(chan struct{})
	var caller Caller
	var getErr error
	go func() {
		caller, getErr = sm.Get(getCtx, activeID, true)
		close(getDone)
	}()

	blocked := false
	select {
	case <-getDone:
	case <-time.After(300 * time.Millisecond):
		blocked = true
	}

	close(conn.releaseWrite)
	select {
	case <-httpErrCh:
	case <-time.After(2 * time.Second):
		t.Fatal("HandleHTTPRequest did not exit after unblocking write")
	}

	if blocked {
		select {
		case <-getDone:
		case <-time.After(2 * time.Second):
			t.Fatal("Get did not return after releasing blocked HTTP write")
		}
		t.Fatal("Get call was blocked by unrelated HandleHTTPRequest lock hold despite deadline")
	}

	require.NoError(t, getErr)
	require.NotNil(t, caller)
	require.Equal(t, "existing-key", caller.SharedKey())
}
