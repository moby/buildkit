package session

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moby/buildkit/session/testutil"
	"github.com/stretchr/testify/require"
)

func waitForActiveCaller(t *testing.T, sm *Manager, id string, timeout time.Duration) Caller {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		c, err := sm.Get(ctx, id, true)
		cancel()
		if err == nil && c != nil {
			select {
			case <-c.Context().Done():
			default:
				return c
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("session %s did not become active in time", id)
	return nil
}

func waitForActiveCallerErr(sm *Manager, id string, timeout time.Duration) (Caller, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		c, err := sm.Get(ctx, id, true)
		cancel()
		if err == nil && c != nil {
			select {
			case <-c.Context().Done():
			default:
				return c, nil
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil, fmt.Errorf("session %s did not become active in time", id)
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) {
	return c.reader.Read(p)
}

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

func httpUpgradeTestDialer(sm *Manager) Dialer {
	return func(ctx context.Context, proto string, meta map[string][]string) (net.Conn, error) {
		serverConn, clientConn := net.Pipe()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://session.local/upgrade", nil)
		if err != nil {
			_ = serverConn.Close()
			_ = clientConn.Close()
			return nil, err
		}
		req.Header = make(http.Header)
		for k, v := range meta {
			req.Header[k] = append([]string(nil), v...)
		}
		req.Header.Set("Upgrade", proto)

		rw := &hijackOnlyResponseWriter{conn: serverConn}
		go func() {
			_ = sm.HandleHTTPRequest(ctx, rw, req)
		}()

		reader := bufio.NewReader(clientConn)
		resp, err := http.ReadResponse(reader, req)
		if err != nil {
			_ = serverConn.Close()
			_ = clientConn.Close()
			return nil, err
		}
		if resp.StatusCode != http.StatusSwitchingProtocols {
			_ = serverConn.Close()
			_ = clientConn.Close()
			return nil, fmt.Errorf("unexpected upgrade status: %d", resp.StatusCode)
		}

		return &bufferedConn{Conn: clientConn, reader: reader}, nil
	}
}

func startSessionForTest(t *testing.T, parent context.Context, sm *Manager, id, sharedKey string) (*Session, func()) {
	t.Helper()

	s, err := NewSession(parent, sharedKey)
	require.NoError(t, err)
	if id != "" {
		s.id = id
	}

	dialer := Dialer(testutil.TestStream(testutil.Handler(sm.HandleConn)))
	runCtx, runCancel := context.WithCancelCause(parent)
	runErrCh := make(chan error, 1)
	go func() {
		runErrCh <- s.Run(runCtx, dialer)
	}()

	waitForActiveCaller(t, sm, s.ID(), 2*time.Second)

	cleanup := func() {
		require.NoError(t, s.Close())
		runCancel(context.Canceled)
		select {
		case err := <-runErrCh:
			require.NoError(t, err)
		case <-time.After(2 * time.Second):
			t.Fatalf("session run did not exit in time for %s", s.ID())
		}
	}

	return s, cleanup
}

func TestManagerGetNoWaitMissingSessionReturnsNil(t *testing.T) {
	sm, err := NewManager()
	require.NoError(t, err)

	c, err := sm.Get(t.Context(), "missing-session", true)
	require.NoError(t, err)
	require.Nil(t, c)
}

func TestManagerGetPrefixLookupOnActiveSession(t *testing.T) {
	sm, err := NewManager()
	require.NoError(t, err)

	sessionID := "prefix-target"
	_, cleanup := startSessionForTest(t, t.Context(), sm, sessionID, "prefix-shared-key")
	defer cleanup()

	c, err := sm.Get(t.Context(), "vertex:"+sessionID, false)
	require.NoError(t, err)
	require.NotNil(t, c)
	require.Equal(t, "prefix-shared-key", c.SharedKey())
}

func TestManagerGetContextCancelWhileWaiting(t *testing.T) {
	sm, err := NewManager()
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	c, err := sm.Get(ctx, "missing-cancel", false)
	require.Error(t, err)
	require.Nil(t, c)
	require.Contains(t, err.Error(), `session "missing-cancel" did not start`)
	require.Contains(t, err.Error(), "context deadline exceeded")
}

func TestManagerGetCanceledContextFastPathOnActiveSession(t *testing.T) {
	sm, err := NewManager()
	require.NoError(t, err)

	activeCtx, activeCancel := context.WithCancelCause(t.Context())
	defer activeCancel(context.Canceled)

	const sessionID = "active-fastpath-cancel"
	require.NoError(t, sm.withState(t.Context(), func(state *sessionState) error {
		state.active[sessionID] = &client{
			Session: Session{
				id:        sessionID,
				sharedKey: "active-fastpath-key",
				ctx:       activeCtx,
				done:      make(chan struct{}),
			},
			supported: map[string]struct{}{},
		}
		return nil
	}))

	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(context.Canceled)

	c, err := sm.Get(ctx, sessionID, false)
	require.NoError(t, err)
	require.NotNil(t, c)
	require.Equal(t, "active-fastpath-key", c.SharedKey())
}

func TestManagerGetCanceledContextFastPathNoWait(t *testing.T) {
	sm, err := NewManager()
	require.NoError(t, err)

	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(context.Canceled)

	c, err := sm.Get(ctx, "missing-fastpath-no-wait", true)
	require.NoError(t, err)
	require.Nil(t, c)
}

func TestManagerGetContextCancelCleansPendingWaiters(t *testing.T) {
	sm, err := NewManager()
	require.NoError(t, err)

	const waiters = 200
	var wg sync.WaitGroup
	wg.Add(waiters)

	for i := 0; i < waiters; i++ {
		i := i
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
			defer cancel()
			_, err := sm.Get(ctx, fmt.Sprintf("missing-pending-%d", i), false)
			require.Error(t, err)
		}()
	}

	wg.Wait()

	require.NoError(t, sm.withState(t.Context(), func(state *sessionState) error {
		require.Zero(t, len(state.pending), "timed-out Get waiters should not leak pending channels")
		return nil
	}))
}

func TestManagerGetHighConcurrencyInactiveThenCreate(t *testing.T) {
	sm, err := NewManager()
	require.NoError(t, err)

	const waiters = 300
	sessionID := "inactive-then-create"

	errCh := make(chan error, waiters)
	var startWG sync.WaitGroup
	startWG.Add(waiters)

	for i := 0; i < waiters; i++ {
		go func() {
			startWG.Done()
			ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
			defer cancel()
			c, err := sm.Get(ctx, sessionID, false)
			if err != nil {
				errCh <- err
				return
			}
			if c == nil {
				errCh <- fmt.Errorf("received nil caller")
				return
			}
			errCh <- nil
		}()
	}

	startWG.Wait()
	time.Sleep(75 * time.Millisecond)

	_, cleanup := startSessionForTest(t, t.Context(), sm, sessionID, "inactive-shared")
	defer cleanup()

	for i := 0; i < waiters; i++ {
		require.NoError(t, <-errCh)
	}
}

func TestManagerGetHighConcurrencyAlreadyActive(t *testing.T) {
	sm, err := NewManager()
	require.NoError(t, err)

	sessionID := "already-active"
	_, cleanup := startSessionForTest(t, t.Context(), sm, sessionID, "active-shared")
	defer cleanup()

	const (
		rounds  = 8
		callers = 250
	)
	for round := 0; round < rounds; round++ {
		errCh := make(chan error, callers)
		for i := 0; i < callers; i++ {
			i := i
			go func() {
				noWait := i%2 == 0
				lookupID := sessionID
				if i%5 == 0 {
					lookupID = "vertex:" + sessionID
				}

				ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
				defer cancel()
				c, err := sm.Get(ctx, lookupID, noWait)
				if err != nil {
					errCh <- err
					return
				}
				if c == nil {
					errCh <- fmt.Errorf("nil caller from Get(noWait=%v)", noWait)
					return
				}
				if c.SharedKey() != "active-shared" {
					errCh <- fmt.Errorf("unexpected shared key %q", c.SharedKey())
					return
				}
				errCh <- nil
			}()
		}

		for i := 0; i < callers; i++ {
			require.NoError(t, <-errCh)
		}
	}
}

func TestManagerGetBroadcastCancelStorm(t *testing.T) {
	sm, err := NewManager()
	require.NoError(t, err)

	const waiters = 300
	sessionID := "cancel-storm"

	cancels := make([]context.CancelCauseFunc, 0, waiters)
	errCh := make(chan error, waiters)

	for i := 0; i < waiters; i++ {
		ctx, cancel := context.WithCancelCause(t.Context())
		cancels = append(cancels, cancel)
		go func(ctx context.Context) {
			c, err := sm.Get(ctx, sessionID, false)
			if err == nil {
				errCh <- fmt.Errorf("expected error for canceled waiter")
				return
			}
			if c != nil {
				errCh <- fmt.Errorf("expected nil caller for canceled waiter")
				return
			}
			errCh <- nil
		}(ctx)
	}

	time.Sleep(75 * time.Millisecond)
	for _, cancel := range cancels {
		cancel(context.Canceled)
	}

	for i := 0; i < waiters; i++ {
		select {
		case err := <-errCh:
			require.NoError(t, err)
		case <-time.After(3 * time.Second):
			t.Fatal("timeout waiting for canceled waiters to drain")
		}
	}
}

func TestManagerGetBroadcastNoMissedWakeups(t *testing.T) {
	sm, err := NewManager()
	require.NoError(t, err)

	const (
		rounds  = 15
		waiters = 120
	)

	for round := 0; round < rounds; round++ {
		sessionID := fmt.Sprintf("broadcast-round-%d", round)
		errCh := make(chan error, waiters)
		var startWG sync.WaitGroup
		startWG.Add(waiters)

		for i := 0; i < waiters; i++ {
			go func() {
				startWG.Done()
				ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
				defer cancel()
				c, err := sm.Get(ctx, sessionID, false)
				if err != nil {
					errCh <- err
					return
				}
				if c == nil {
					errCh <- fmt.Errorf("nil caller during broadcast round %d", round)
					return
				}
				errCh <- nil
			}()
		}

		startWG.Wait()
		time.Sleep(50 * time.Millisecond)

		_, cleanup := startSessionForTest(t, t.Context(), sm, sessionID, fmt.Sprintf("round-%d", round))
		for i := 0; i < waiters; i++ {
			require.NoError(t, <-errCh)
		}
		cleanup()
	}
}

func TestManagerHandleConnDuplicateSessionIDOverwritesExisting(t *testing.T) {
	// Known failing hypothesis:
	// HandleConn does not reject duplicate session IDs, so the second registration overwrites
	// sm.sessions[id] while the first session is still active.
	// Fix direction: enforce duplicate-ID rejection in HandleConn/handleConn (matching HTTP path
	// behavior), or otherwise guarantee first-writer-wins semantics without map replacement races.
	sm, err := NewManager()
	require.NoError(t, err)

	dialer := Dialer(testutil.TestStream(testutil.Handler(sm.HandleConn)))
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(context.Canceled)

	s1, err := NewSession(ctx, "first-shared")
	require.NoError(t, err)
	s2, err := NewSession(ctx, "second-shared")
	require.NoError(t, err)

	const duplicatedID = "duplicate-session-id"
	s1.id = duplicatedID
	s2.id = duplicatedID

	errCh1 := make(chan error, 1)
	errCh2 := make(chan error, 1)
	go func() { errCh1 <- s1.Run(ctx, dialer) }()
	waitForActiveCaller(t, sm, duplicatedID, 2*time.Second)

	go func() { errCh2 <- s2.Run(ctx, dialer) }()
	time.Sleep(150 * time.Millisecond)

	// This asserts desired behavior and intentionally exposes current overwrite behavior.
	c := waitForActiveCaller(t, sm, duplicatedID, 2*time.Second)
	require.Equal(t, "first-shared", c.SharedKey(), "duplicate session registration should not replace existing active session")

	require.NoError(t, s2.Close())
	require.NoError(t, s1.Close())
	cancel(context.Canceled)

	require.NoError(t, <-errCh1)
	require.NoError(t, <-errCh2)
}

func TestManagerParallelCreateGetNeedFiveSecondChurn(t *testing.T) {
	sm, err := NewManager()
	require.NoError(t, err)

	dialer := Dialer(testutil.TestStream(testutil.Handler(sm.HandleConn)))
	deadline := time.Now().Add(5 * time.Second)
	workers := runtime.GOMAXPROCS(0) * 4
	if workers < 8 {
		workers = 8
	}

	errCh := make(chan error, workers*16)
	var seq atomic.Int64
	var wg sync.WaitGroup
	wg.Add(workers)

	for worker := 0; worker < workers; worker++ {
		worker := worker
		go func() {
			defer wg.Done()
			localCounter := int64(0)

			for time.Now().Before(deadline) {
				localCounter++
				id := fmt.Sprintf("churn-%d-%d", worker, localCounter)
				sharedKey := "key-" + id

				needCtx, needCancel := context.WithTimeout(t.Context(), 2*time.Second)
				needDone := make(chan error, 1)
				go func() {
					c, err := sm.Get(needCtx, id, false)
					if err != nil {
						needDone <- fmt.Errorf("need Get failed for %s: %w", id, err)
						return
					}
					if c == nil {
						needDone <- fmt.Errorf("need Get returned nil caller for %s", id)
						return
					}
					needDone <- nil
				}()

				s, err := NewSession(t.Context(), sharedKey)
				if err != nil {
					needCancel()
					errCh <- err
					return
				}
				s.id = id

				runCtx, runCancel := context.WithCancelCause(t.Context())
				runDone := make(chan error, 1)
				go func() {
					runDone <- s.Run(runCtx, dialer)
				}()

				if _, err := waitForActiveCallerErr(sm, id, 2*time.Second); err != nil {
					_ = s.Close()
					runCancel(context.Canceled)
					select {
					case <-runDone:
					case <-time.After(2 * time.Second):
					}
					needCancel()
					errCh <- err
					return
				}

				select {
				case err := <-needDone:
					if err != nil {
						_ = s.Close()
						runCancel(context.Canceled)
						select {
						case <-runDone:
						case <-time.After(2 * time.Second):
						}
						needCancel()
						errCh <- err
						return
					}
				case <-time.After(2 * time.Second):
					_ = s.Close()
					runCancel(context.Canceled)
					select {
					case <-runDone:
					case <-time.After(2 * time.Second):
					}
					needCancel()
					errCh <- fmt.Errorf("timed out waiting for need Get on %s", id)
					return
				}
				needCancel()

				for i := 0; i < 4; i++ {
					lookupID := id
					if i%2 == 1 {
						lookupID = "vertex:" + id
					}
					getCtx, getCancel := context.WithTimeout(t.Context(), 2*time.Second)
					c, err := sm.Get(getCtx, lookupID, i%2 == 0)
					getCancel()
					if err != nil {
						_ = s.Close()
						runCancel(context.Canceled)
						select {
						case <-runDone:
						case <-time.After(2 * time.Second):
						}
						errCh <- fmt.Errorf("active Get failed for %s: %w", id, err)
						return
					}
					if c == nil {
						_ = s.Close()
						runCancel(context.Canceled)
						select {
						case <-runDone:
						case <-time.After(2 * time.Second):
						}
						errCh <- fmt.Errorf("active Get returned nil caller for %s", id)
						return
					}
				}

				if err := s.Close(); err != nil {
					runCancel(context.Canceled)
					select {
					case <-runDone:
					case <-time.After(2 * time.Second):
					}
					errCh <- err
					return
				}
				runCancel(context.Canceled)
				select {
				case err := <-runDone:
					if err != nil {
						errCh <- err
						return
					}
				case <-time.After(2 * time.Second):
					errCh <- fmt.Errorf("session run did not exit in time for %s", id)
					return
				}

				seq.Add(1)
			}
		}()
	}

	wg.Wait()
	close(errCh)
	for err := range errCh {
		require.NoError(t, err)
	}
	require.Greater(t, seq.Load(), int64(workers), "expected churn loop to perform multiple operations")
}

func TestManagerGetParallelPairsWakeupsHTTPAndRawFiveSeconds(t *testing.T) {
	sm, err := NewManager()
	require.NoError(t, err)

	rawDialer := Dialer(testutil.TestStream(testutil.Handler(sm.HandleConn)))
	httpDialer := httpUpgradeTestDialer(sm)

	deadline := time.Now().Add(5 * time.Second)
	pairs := runtime.GOMAXPROCS(0) * 3
	if pairs < 12 {
		pairs = 12
	}

	errCh := make(chan error, pairs*32)
	var loops atomic.Int64
	var wg sync.WaitGroup
	wg.Add(pairs)

	for pairID := 0; pairID < pairs; pairID++ {
		pairID := pairID
		go func() {
			defer wg.Done()
			localIter := 0
			for time.Now().Before(deadline) {
				localIter++
				sessionID := fmt.Sprintf("pair-session-%d-%d", pairID, localIter)
				sharedKey := fmt.Sprintf("pair-key-%d-%d", pairID, localIter)

				waiterStarted := make(chan struct{})
				waiterErrCh := make(chan error, 1)
				waiterCtx, waiterCancel := context.WithTimeout(t.Context(), 2*time.Second)
				go func() {
					close(waiterStarted)
					c, err := sm.Get(waiterCtx, sessionID, false)
					if err != nil {
						waiterErrCh <- fmt.Errorf("blocking Get failed for %s: %w", sessionID, err)
						return
					}
					if c == nil {
						waiterErrCh <- fmt.Errorf("blocking Get returned nil for %s", sessionID)
						return
					}
					if c.SharedKey() != sharedKey {
						waiterErrCh <- fmt.Errorf("blocking Get shared key mismatch for %s: %q", sessionID, c.SharedKey())
						return
					}
					waiterErrCh <- nil
				}()

				<-waiterStarted

				creatorRelease := make(chan struct{})
				creatorErrCh := make(chan error, 1)
				useHTTP := (pairID+localIter)%2 == 0
				go func() {
					s, err := NewSession(t.Context(), sharedKey)
					if err != nil {
						creatorErrCh <- err
						return
					}
					s.id = sessionID

					runCtx, runCancel := context.WithCancelCause(t.Context())
					runErrCh := make(chan error, 1)
					dialer := rawDialer
					if useHTTP {
						dialer = httpDialer
					}
					go func() {
						runErrCh <- s.Run(runCtx, dialer)
					}()

					if _, err := waitForActiveCallerErr(sm, sessionID, 2*time.Second); err != nil {
						_ = s.Close()
						runCancel(context.Canceled)
						select {
						case <-runErrCh:
						case <-time.After(2 * time.Second):
						}
						creatorErrCh <- err
						return
					}

					<-creatorRelease

					if err := s.Close(); err != nil {
						runCancel(context.Canceled)
						select {
						case <-runErrCh:
						case <-time.After(2 * time.Second):
						}
						creatorErrCh <- err
						return
					}
					runCancel(context.Canceled)
					select {
					case err := <-runErrCh:
						if err != nil {
							creatorErrCh <- err
							return
						}
					case <-time.After(2 * time.Second):
						creatorErrCh <- fmt.Errorf("session run did not exit for %s", sessionID)
						return
					}

					creatorErrCh <- nil
				}()

				var waiterErr error
				select {
				case waiterErr = <-waiterErrCh:
				case <-time.After(2 * time.Second):
					waiterErr = fmt.Errorf("timeout waiting for blocking Get for %s", sessionID)
				}
				waiterCancel()

				close(creatorRelease)
				var creatorErr error
				select {
				case creatorErr = <-creatorErrCh:
				case <-time.After(3 * time.Second):
					creatorErr = fmt.Errorf("timeout waiting for creator cleanup for %s", sessionID)
				}

				if waiterErr != nil {
					errCh <- waiterErr
					return
				}
				if creatorErr != nil {
					errCh <- creatorErr
					return
				}

				loops.Add(1)
			}
		}()
	}

	wg.Wait()
	close(errCh)
	for err := range errCh {
		require.NoError(t, err)
	}
	require.Greater(t, loops.Load(), int64(pairs), "expected each pair worker to complete multiple loops")
}

func TestManagerHandleHTTPRequestDoesNotTreatClosedSessionAsActive(t *testing.T) {
	sm, err := NewManager()
	require.NoError(t, err)

	sessionID := "closed-http-recreate"
	closedCtx, closedCancel := context.WithCancelCause(context.Background())
	closedCancel(context.Canceled)

	require.NoError(t, sm.withState(t.Context(), func(state *sessionState) error {
		state.active[sessionID] = &client{
			Session: Session{
				id:   sessionID,
				ctx:  closedCtx,
				done: make(chan struct{}),
			},
			supported: map[string]struct{}{},
		}
		return nil
	}))

	s, err := NewSession(t.Context(), "new-http-shared")
	require.NoError(t, err)
	s.id = sessionID

	runCtx, runCancel := context.WithCancelCause(t.Context())
	defer runCancel(context.Canceled)
	runDone := make(chan error, 1)
	go func() {
		runDone <- s.Run(runCtx, httpUpgradeTestDialer(sm))
	}()

	c, err := waitForActiveCallerErr(sm, sessionID, 2*time.Second)
	require.NoError(t, err, "a closed stale session should not block HTTP re-registration")
	require.NotNil(t, c)
	require.Equal(t, "new-http-shared", c.SharedKey())

	require.NoError(t, s.Close())
	runCancel(context.Canceled)
	select {
	case err := <-runDone:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatalf("session run did not exit in time for %s", sessionID)
	}
}

func TestManagerHandleHTTPRequestDoesNotBlockUnrelatedGet(t *testing.T) {
	sm, err := NewManager()
	require.NoError(t, err)

	activeCtx, activeCancel := context.WithCancelCause(t.Context())
	defer activeCancel(context.Canceled)

	const activeID = "existing-active"
	require.NoError(t, sm.withState(t.Context(), func(state *sessionState) error {
		state.active[activeID] = &client{
			Session: Session{
				id:        activeID,
				sharedKey: "existing-key",
				ctx:       activeCtx,
				done:      make(chan struct{}),
			},
			supported: map[string]struct{}{},
		}
		return nil
	}))

	conn := &blockingWriteConn{
		writeStarted: make(chan struct{}),
		releaseWrite: make(chan struct{}),
	}
	reqCtx, reqCancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
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
		// HandleHTTPRequest is blocked in response write.
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for HandleHTTPRequest response write")
	}

	getCtx, getCancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer getCancel()
	getDone := make(chan struct{})
	var c Caller
	var getErr error
	go func() {
		c, getErr = sm.Get(getCtx, activeID, true)
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

	require.NoError(t, getErr, "unrelated active session lookup should not be blocked by another handshake")
	require.NotNil(t, c)
	require.Equal(t, "existing-key", c.SharedKey())
}

func TestManagerGetDoesNotBlockOnMutexPastDeadline(t *testing.T) {
	sm, err := NewManager()
	require.NoError(t, err)

	held := <-sm.state
	defer func() { sm.state <- held }()

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		_, _ = sm.Get(ctx, "never-available", true)
		close(done)
	}()

	select {
	case <-done:
		// Good: returned despite state lock contention.
	case <-time.After(300 * time.Millisecond):
		t.Fatal("Get remained blocked on state lock past context deadline")
	}
}
