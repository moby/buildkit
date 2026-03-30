package session

import (
	"context"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/pkg/errors"
	"google.golang.org/grpc"
)

// Caller can invoke requests on the session
type Caller interface {
	Context() context.Context
	Supports(method string) bool
	Conn() *grpc.ClientConn
	SharedKey() string
}

type client struct {
	Session
	cc        *grpc.ClientConn
	supported map[string]struct{}
}

type sessionState struct {
	active   map[string]*client
	pending  map[string]*pendingWaiters
	reserved map[string]struct{}
}

type pendingWaiters struct {
	ch      chan struct{}
	waiters int
}

func newSessionState() *sessionState {
	return &sessionState{
		active:   make(map[string]*client),
		pending:  make(map[string]*pendingWaiters),
		reserved: make(map[string]struct{}),
	}
}

func (s *sessionState) getActive(id string) *client {
	c, ok := s.active[id]
	if !ok {
		return nil
	}
	if c.closed() {
		delete(s.active, id)
		return nil
	}
	return c
}

func (s *sessionState) reserve(id string) error {
	if c := s.getActive(id); c != nil {
		return errors.Errorf("session %s already exists", id)
	}
	if _, ok := s.reserved[id]; ok {
		return errors.Errorf("session %s already exists", id)
	}
	s.reserved[id] = struct{}{}
	return nil
}

func (s *sessionState) activate(id string, c *client) {
	s.active[id] = c
	delete(s.reserved, id)
	s.notifyPending(id)
}

func (s *sessionState) remove(id string) {
	delete(s.active, id)
	delete(s.reserved, id)
}

func (s *sessionState) pendingWait(id string) chan struct{} {
	pw, ok := s.pending[id]
	if !ok {
		pw = &pendingWaiters{ch: make(chan struct{})}
		s.pending[id] = pw
	}
	pw.waiters++
	return pw.ch
}

func (s *sessionState) releasePendingWait(id string, ch chan struct{}) {
	pw, ok := s.pending[id]
	if !ok || pw.ch != ch {
		return
	}
	pw.waiters--
	if pw.waiters <= 0 {
		delete(s.pending, id)
	}
}

func (s *sessionState) notifyPending(id string) {
	pw, ok := s.pending[id]
	if !ok {
		return
	}
	delete(s.pending, id)
	close(pw.ch)
}

// Manager is a controller for accessing currently active sessions
type Manager struct {
	state chan *sessionState
}

type sessionCallbacks struct {
	setup   func(ctx context.Context, state *sessionState) error
	session func(ctx context.Context) error
	cleanup func(state *sessionState)
}

// NewManager returns a new Manager
func NewManager() (*Manager, error) {
	sm := &Manager{
		state: make(chan *sessionState, 1),
	}
	sm.state <- newSessionState()
	return sm, nil
}

func (sm *Manager) withState(ctx context.Context, fn func(state *sessionState) error) error {
	var state *sessionState
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case state = <-sm.state:
	}
	defer func() {
		sm.state <- state
	}()
	return fn(state)
}

func (sm *Manager) tryWithState(fn func(state *sessionState) error) (bool, error) {
	select {
	case state := <-sm.state:
		defer func() {
			sm.state <- state
		}()
		return true, fn(state)
	default:
		return false, nil
	}
}

func (sm *Manager) runSession(ctx context.Context, cb sessionCallbacks) (retErr error) {
	if cb.setup != nil {
		if err := sm.withState(ctx, func(state *sessionState) error {
			return cb.setup(ctx, state)
		}); err != nil {
			return errors.Wrapf(err, "setting up session")
		}
	}
	if cb.cleanup != nil {
		defer func() {
			err := sm.withState(context.Background(), func(state *sessionState) error {
				cb.cleanup(state)
				return nil
			})
			if retErr == nil && err != nil {
				retErr = err
			}
		}()
	}
	if cb.session == nil {
		return nil
	}
	return cb.session(ctx)
}

// HandleHTTPRequest handles an incoming HTTP request
func (sm *Manager) HandleHTTPRequest(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		return errors.New("handler does not support hijack")
	}

	id := r.Header.Get(headerSessionID)

	proto := r.Header.Get("Upgrade")

	if proto == "" {
		return errors.New("no upgrade proto in request")
	}

	if proto != "h2c" {
		return errors.Errorf("protocol %s not supported", proto)
	}

	var conn net.Conn
	return sm.runSession(ctx, sessionCallbacks{
		setup: func(_ context.Context, state *sessionState) error {
			return state.reserve(id)
		},
		session: func(ctx context.Context) error {
			var err error
			conn, _, err = hijacker.Hijack()
			if err != nil {
				return errors.Wrap(err, "failed to hijack connection")
			}

			resp := &http.Response{
				StatusCode: http.StatusSwitchingProtocols,
				ProtoMajor: 1,
				ProtoMinor: 1,
				Header:     http.Header{},
			}
			resp.Header.Set("Connection", "Upgrade")
			resp.Header.Set("Upgrade", proto)

			// set raw mode
			if _, err := conn.Write([]byte{}); err != nil {
				_ = conn.Close()
				return errors.Wrap(err, "failed to switch connection to raw mode")
			}
			if err := resp.Write(conn); err != nil {
				_ = conn.Close()
				return errors.Wrap(err, "failed to write upgrade response")
			}
			return sm.serveConnSession(ctx, conn, r.Header)
		},
		cleanup: func(state *sessionState) {
			state.remove(id)
		},
	})
}

// HandleConn handles an incoming raw connection
func (sm *Manager) HandleConn(ctx context.Context, conn net.Conn, opts map[string][]string) error {
	opts = canonicalHeaders(opts)
	id := http.Header(opts).Get(headerSessionID)
	return sm.runSession(ctx, sessionCallbacks{
		setup: func(_ context.Context, state *sessionState) error {
			return state.reserve(id)
		},
		session: func(ctx context.Context) error {
			return sm.serveConnSession(ctx, conn, opts)
		},
		cleanup: func(state *sessionState) {
			state.remove(id)
		},
	})
}

func (sm *Manager) serveConnSession(ctx context.Context, conn net.Conn, opts map[string][]string) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer func() { cancel(errors.WithStack(context.Canceled)) }()

	opts = canonicalHeaders(opts)

	h := http.Header(opts)
	id := h.Get(headerSessionID)
	sharedKey := h.Get(headerSessionSharedKey)

	ctx, cc, err := grpcClientConn(ctx, conn)
	if err != nil {
		return errors.Wrapf(err, "creating gRPC client connection for session %q", id)
	}

	c := &client{
		Session: Session{
			id:        id,
			sharedKey: sharedKey,
			ctx:       ctx,
			cancelCtx: cancel,
			done:      make(chan struct{}),
		},
		cc:        cc,
		supported: make(map[string]struct{}),
	}

	for _, m := range opts[headerSessionMethod] {
		c.supported[strings.ToLower(m)] = struct{}{}
	}
	defer conn.Close()
	defer close(c.done)

	if err := sm.withState(ctx, func(state *sessionState) error {
		state.activate(id, c)
		return nil
	}); err != nil {
		return errors.Wrapf(err, "activating session %q", id)
	}

	<-c.ctx.Done()
	return nil
}

// Get returns a session by ID
func (sm *Manager) Get(ctx context.Context, id string, noWait bool) (Caller, error) {
	// session prefix is used to identify vertexes with different contexts so
	// they would not collide, but for lookup we don't need the prefix
	if p := strings.SplitN(id, ":", 2); len(p) == 2 && len(p[1]) > 0 {
		id = p[1]
	}

	start := time.Now()

	// Fast path: do a single non-blocking state check. This allows immediate
	// success for active/noWait lookups even when ctx is already canceled.
	var c *client
	ok, err := sm.tryWithState(func(state *sessionState) error {
		c = state.getActive(id)
		return nil
	})
	if err != nil {
		return nil, errors.Wrapf(context.Cause(ctx), "looking up active session for %q", id)
	}
	if ok {
		if c != nil {
			return c, nil
		}
		if noWait {
			return nil, nil
		}
		if context.Cause(ctx) != nil {
			return nil, errors.Wrapf(context.Cause(ctx), "cannot get session %q; context already terminated", id)
		}
	}

	for {
		var waitCh chan struct{}
		err := sm.withState(ctx, func(state *sessionState) error {
			c = state.getActive(id)
			if c != nil || noWait {
				return nil
			}
			waitCh = state.pendingWait(id)
			return nil
		})
		if err != nil {
			return nil, errors.Wrapf(context.Cause(ctx), "looking up active session for %q", id)
		}
		if c != nil {
			return c, nil
		}
		if noWait {
			return nil, nil
		}
		select {
		case <-ctx.Done():
			_ = sm.withState(context.Background(), func(state *sessionState) error {
				state.releasePendingWait(id, waitCh)
				return nil
			})
			return nil, errors.Wrapf(context.Cause(ctx), "session %q did not start (waited for %s)", id, time.Since(start).Truncate(time.Millisecond))
		case <-waitCh:
		}
	}
}

func (c *client) Context() context.Context {
	return c.context()
}

func (c *client) SharedKey() string {
	return c.sharedKey
}

func (c *client) Supports(url string) bool {
	_, ok := c.supported[strings.ToLower(url)]
	return ok
}
func (c *client) Conn() *grpc.ClientConn {
	return c.cc
}

func canonicalHeaders(in map[string][]string) map[string][]string {
	out := map[string][]string{}
	for k := range in {
		out[http.CanonicalHeaderKey(k)] = in[k]
	}
	return out
}
