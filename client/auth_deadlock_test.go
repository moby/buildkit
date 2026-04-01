package client

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/session"
	sessionauth "github.com/moby/buildkit/session/auth"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/nacl/sign"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestAuthDeadlockIntegration(t *testing.T) {
	integration.Run(t, integration.TestFuncs(
		testAuthDeadlockRepro,
		testAuthDeadlockBlackholedSessionTransport,
		testAuthDeadlockBlackholedCredentialsFallback,
		testAuthDeadlockBlackholedFetchTokenAuthority,
		testAuthDeadlockVerifyTokenAuthorityReuse,
	))
}

type authChallengeRegistry struct {
	server *httptest.Server
	host   string
	ref    string
	token  string
}

func newAuthChallengeRegistry(t *testing.T) *authChallengeRegistry {
	t.Helper()

	r := &authChallengeRegistry{
		token: "test-token",
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.URL.Path == "/token":
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(map[string]any{
				"token":      r.token,
				"expires_in": 60,
			}); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			return
		case strings.HasPrefix(req.URL.Path, "/v2/"):
			if req.Header.Get("Authorization") != "Bearer "+r.token {
				w.Header().Set("Www-Authenticate", fmt.Sprintf(`Bearer realm="%s/token",service="deadlock-registry",scope="repository:deadlock/test:pull"`, r.server.URL))
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if req.URL.Path == "/v2/" {
				w.WriteHeader(http.StatusOK)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"errors":[{"code":"MANIFEST_UNKNOWN","message":"manifest unknown"}]}`))
			return
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	r.server = httptest.NewServer(handler)
	t.Cleanup(r.server.Close)

	u, err := url.Parse(r.server.URL)
	require.NoError(t, err)
	r.host = u.Host
	r.ref = r.host + "/deadlock/test:latest"
	return r
}

type controlledAuthProvider struct {
	sessionauth.UnimplementedAuthServer

	host        string
	blockLookup bool
	started     chan struct{}
	release     chan struct{}
}

func (p *controlledAuthProvider) Register(server *grpc.Server) {
	sessionauth.RegisterAuthServer(server, p)
}

func (p *controlledAuthProvider) Credentials(_ context.Context, req *sessionauth.CredentialsRequest) (*sessionauth.CredentialsResponse, error) {
	if req.Host != p.host {
		return &sessionauth.CredentialsResponse{}, nil
	}
	return &sessionauth.CredentialsResponse{
		Username: "user",
		Secret:   "secret",
	}, nil
}

func (p *controlledAuthProvider) GetTokenAuthority(_ context.Context, req *sessionauth.GetTokenAuthorityRequest) (*sessionauth.GetTokenAuthorityResponse, error) {
	if req.Host == p.host && p.blockLookup {
		select {
		case <-p.started:
		default:
			close(p.started)
		}
		<-p.release
	}
	return nil, status.Error(codes.Unimplemented, "force credentials fallback")
}

type gatedAuthProvider struct {
	sessionauth.UnimplementedAuthServer

	host          string
	lookupStarted chan struct{}
	lookupResume  chan struct{}
}

func (p *gatedAuthProvider) Register(server *grpc.Server) {
	sessionauth.RegisterAuthServer(server, p)
}

func (p *gatedAuthProvider) Credentials(_ context.Context, req *sessionauth.CredentialsRequest) (*sessionauth.CredentialsResponse, error) {
	if req.Host != p.host {
		return &sessionauth.CredentialsResponse{}, nil
	}
	return &sessionauth.CredentialsResponse{
		Username: "user",
		Secret:   "secret",
	}, nil
}

func (p *gatedAuthProvider) GetTokenAuthority(_ context.Context, req *sessionauth.GetTokenAuthorityRequest) (*sessionauth.GetTokenAuthorityResponse, error) {
	if req.Host == p.host {
		select {
		case <-p.lookupStarted:
		default:
			close(p.lookupStarted)
		}
		<-p.lookupResume
	}
	return nil, status.Error(codes.Unimplemented, "force credentials fallback")
}

type gatedCredentialsProvider struct {
	sessionauth.UnimplementedAuthServer

	host              string
	credentialsStart  chan struct{}
	credentialsResume chan struct{}
}

func (p *gatedCredentialsProvider) Register(server *grpc.Server) {
	sessionauth.RegisterAuthServer(server, p)
}

func (p *gatedCredentialsProvider) Credentials(_ context.Context, req *sessionauth.CredentialsRequest) (*sessionauth.CredentialsResponse, error) {
	if req.Host != p.host {
		return &sessionauth.CredentialsResponse{}, nil
	}
	select {
	case <-p.credentialsStart:
	default:
		close(p.credentialsStart)
	}
	<-p.credentialsResume
	return &sessionauth.CredentialsResponse{
		Username: "user",
		Secret:   "secret",
	}, nil
}

func (p *gatedCredentialsProvider) GetTokenAuthority(_ context.Context, req *sessionauth.GetTokenAuthorityRequest) (*sessionauth.GetTokenAuthorityResponse, error) {
	if req.Host == p.host {
		return nil, status.Error(codes.Unimplemented, "force credentials fallback")
	}
	return nil, status.Error(codes.Unimplemented, "not implemented")
}

type authorityAuthProvider struct {
	sessionauth.UnimplementedAuthServer

	host          string
	privateKey    ed25519.PrivateKey
	token         string
	fetchStarted  chan struct{}
	fetchResume   chan struct{}
	verifyStarted chan struct{}
	verifyResume  chan struct{}
}

func (p *authorityAuthProvider) Register(server *grpc.Server) {
	sessionauth.RegisterAuthServer(server, p)
}

func (p *authorityAuthProvider) Credentials(_ context.Context, req *sessionauth.CredentialsRequest) (*sessionauth.CredentialsResponse, error) {
	if req.Host != p.host {
		return &sessionauth.CredentialsResponse{}, nil
	}
	return &sessionauth.CredentialsResponse{}, nil
}

func (p *authorityAuthProvider) GetTokenAuthority(_ context.Context, req *sessionauth.GetTokenAuthorityRequest) (*sessionauth.GetTokenAuthorityResponse, error) {
	if req.Host != p.host {
		return nil, status.Error(codes.Unimplemented, "not implemented")
	}
	return &sessionauth.GetTokenAuthorityResponse{
		PublicKey: append([]byte(nil), p.privateKey[32:]...),
	}, nil
}

func (p *authorityAuthProvider) FetchToken(ctx context.Context, req *sessionauth.FetchTokenRequest) (*sessionauth.FetchTokenResponse, error) {
	if req.Host != p.host {
		return nil, status.Error(codes.Unimplemented, "not implemented")
	}
	if p.fetchStarted != nil {
		select {
		case <-p.fetchStarted:
		default:
			close(p.fetchStarted)
		}
	}
	if p.fetchResume != nil {
		<-p.fetchResume
	}
	return &sessionauth.FetchTokenResponse{
		Token:     p.token,
		ExpiresIn: 60,
	}, nil
}

func (p *authorityAuthProvider) VerifyTokenAuthority(_ context.Context, req *sessionauth.VerifyTokenAuthorityRequest) (*sessionauth.VerifyTokenAuthorityResponse, error) {
	if req.Host != p.host {
		return nil, status.Error(codes.Unimplemented, "not implemented")
	}
	if p.verifyStarted != nil {
		select {
		case <-p.verifyStarted:
		default:
			close(p.verifyStarted)
		}
	}
	if p.verifyResume != nil {
		<-p.verifyResume
	}
	priv := new([64]byte)
	copy((*priv)[:], p.privateKey)
	return &sessionauth.VerifyTokenAuthorityResponse{
		Signed: sign.Sign(nil, req.Payload, priv),
	}, nil
}

func newAuthorityPrivateKey(fill byte) ed25519.PrivateKey {
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = fill
	}
	return ed25519.NewKeyFromSeed(seed)
}

type blackholeConn struct {
	net.Conn

	mu        sync.RWMutex
	blackhole bool
	done      chan struct{}
	closeOnce sync.Once
}

func newBlackholeConn(conn net.Conn) *blackholeConn {
	return &blackholeConn{
		Conn: conn,
		done: make(chan struct{}),
	}
}

func (c *blackholeConn) Activate() {
	c.mu.Lock()
	c.blackhole = true
	c.mu.Unlock()
}

func (c *blackholeConn) isBlackholed() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.blackhole
}

func (c *blackholeConn) Read(p []byte) (int, error) {
	if c.isBlackholed() {
		<-c.done
		return 0, io.EOF
	}
	return c.Conn.Read(p)
}

func (c *blackholeConn) Write(p []byte) (int, error) {
	if c.isBlackholed() {
		<-c.done
		return 0, io.EOF
	}
	return c.Conn.Write(p)
}

func (c *blackholeConn) Close() error {
	var err error
	c.closeOnce.Do(func() {
		close(c.done)
		err = c.Conn.Close()
	})
	return err
}

type blackholeSessionDialer struct {
	base session.Dialer

	mu   sync.Mutex
	conn *blackholeConn
}

func newBlackholeSessionDialer(base session.Dialer) *blackholeSessionDialer {
	return &blackholeSessionDialer{base: base}
}

func (d *blackholeSessionDialer) Dial(ctx context.Context, proto string, meta map[string][]string) (net.Conn, error) {
	conn, err := d.base(ctx, proto, meta)
	if err != nil {
		return nil, err
	}
	wrapped := newBlackholeConn(conn)
	d.mu.Lock()
	d.conn = wrapped
	d.mu.Unlock()
	return wrapped, nil
}

func (d *blackholeSessionDialer) Activate() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.conn != nil {
		d.conn.Activate()
	}
}

func solveImageWithAuth(ctx context.Context, c *Client, imageRef string, authProvider session.Attachable) error {
	st := llb.Image(imageRef)
	def, err := st.Marshal(ctx)
	if err != nil {
		return err
	}
	_, err = c.Solve(ctx, def, SolveOpt{
		Session: []session.Attachable{authProvider},
	}, nil)
	return err
}

func solveImageWithSharedSession(ctx context.Context, c *Client, imageRef string, shared *session.Session) error {
	st := llb.Image(imageRef)
	def, err := st.Marshal(ctx)
	if err != nil {
		return err
	}
	_, err = c.Solve(ctx, def, SolveOpt{
		SharedSession:         shared,
		SessionPreInitialized: true,
	}, nil)
	return err
}

func startSharedAuthSession(t *testing.T, provider session.Attachable, dialer session.Dialer) (*session.Session, func()) {
	t.Helper()

	s, err := session.NewSession(t.Context(), "auth-deadlock")
	require.NoError(t, err)
	s.Allow(provider)

	runCtx, runCancel := context.WithCancelCause(t.Context())
	runErrCh := make(chan error, 1)
	go func() {
		runErrCh <- s.Run(runCtx, dialer)
	}()

	cleanup := func() {
		require.NoError(t, s.Close())
		runCancel(context.Canceled)
		select {
		case err := <-runErrCh:
			require.NoError(t, err)
		case <-time.After(10 * time.Second):
			t.Fatalf("session run did not exit in time for %s", s.ID())
		}
	}

	return s, cleanup
}

func testAuthDeadlockRepro(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)

	registry := newAuthChallengeRegistry(t)

	blockedClient, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer blockedClient.Close()

	fastClient, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer fastClient.Close()

	blockedProvider := &controlledAuthProvider{
		host:        registry.host,
		blockLookup: true,
		started:     make(chan struct{}),
		release:     make(chan struct{}),
	}
	defer func() {
		select {
		case <-blockedProvider.release:
		default:
			close(blockedProvider.release)
		}
	}()

	fastProvider := &controlledAuthProvider{
		host:    registry.host,
		started: make(chan struct{}),
		release: make(chan struct{}),
	}

	firstCtx, cancelFirst := context.WithTimeoutCause(sb.Context(), 30*time.Second, context.DeadlineExceeded)
	defer cancelFirst()

	firstDone := make(chan error, 1)
	go func() {
		firstDone <- solveImageWithAuth(firstCtx, blockedClient, registry.ref, blockedProvider)
	}()

	select {
	case <-blockedProvider.started:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for first solve to enter auth callback")
	}

	secondDone := make(chan error, 1)
	secondStart := time.Now()
	go func() {
		secondDone <- solveImageWithAuth(sb.Context(), fastClient, registry.ref, fastProvider)
	}()

	select {
	case err := <-secondDone:
		require.Error(t, err)
		require.Less(t, time.Since(secondStart), 2*time.Second, "second solve should not be blocked by unrelated hung auth callback")
	case <-time.After(2 * time.Second):
		t.Fatal("second solve blocked behind unrelated hung auth callback")
	}

	close(blockedProvider.release)
	select {
	case err := <-firstDone:
		require.Error(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("first solve did not finish after releasing auth callback")
	}
}

func testAuthDeadlockBlackholedSessionTransport(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)

	registry := newAuthChallengeRegistry(t)

	blockedClient, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer blockedClient.Close()

	fastClient, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer fastClient.Close()

	gatedProvider := &gatedAuthProvider{
		host:          registry.host,
		lookupStarted: make(chan struct{}),
		lookupResume:  make(chan struct{}),
	}
	fastProvider := &controlledAuthProvider{
		host:    registry.host,
		started: make(chan struct{}),
		release: make(chan struct{}),
	}

	blackholeDialer := newBlackholeSessionDialer(blockedClient.Dialer())
	sharedSession, cleanupSession := startSharedAuthSession(t, gatedProvider, blackholeDialer.Dial)

	firstCtx, cancelFirst := context.WithTimeoutCause(sb.Context(), 30*time.Second, context.DeadlineExceeded)
	defer cancelFirst()

	firstDone := make(chan error, 1)
	go func() {
		firstDone <- solveImageWithSharedSession(firstCtx, blockedClient, registry.ref, sharedSession)
	}()

	select {
	case <-gatedProvider.lookupStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for first solve to enter auth callback before blackholing session")
	}

	blackholeDialer.Activate()
	close(gatedProvider.lookupResume)

	secondDone := make(chan error, 1)
	secondStart := time.Now()
	go func() {
		secondDone <- solveImageWithAuth(sb.Context(), fastClient, registry.ref, fastProvider)
	}()

	select {
	case err := <-secondDone:
		require.Error(t, err)
		require.Less(t, time.Since(secondStart), 2*time.Second, "second solve should not be blocked by blackholed session transport on another client")
	case <-time.After(2 * time.Second):
		t.Fatal("second solve blocked behind blackholed auth session transport")
	}

	cleanupSession()
	select {
	case err := <-firstDone:
		require.Error(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("first solve did not finish after blackholed session transport cleanup")
	}
}

func testAuthDeadlockBlackholedCredentialsFallback(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)

	registry := newAuthChallengeRegistry(t)

	blockedClient, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer blockedClient.Close()

	fastClient, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer fastClient.Close()

	credsProvider := &gatedCredentialsProvider{
		host:              registry.host,
		credentialsStart:  make(chan struct{}),
		credentialsResume: make(chan struct{}),
	}
	fastProvider := &controlledAuthProvider{
		host:    registry.host,
		started: make(chan struct{}),
		release: make(chan struct{}),
	}

	blackholeDialer := newBlackholeSessionDialer(blockedClient.Dialer())
	sharedSession, cleanupSession := startSharedAuthSession(t, credsProvider, blackholeDialer.Dial)

	firstCtx, cancelFirst := context.WithTimeoutCause(sb.Context(), 30*time.Second, context.DeadlineExceeded)
	defer cancelFirst()

	firstDone := make(chan error, 1)
	go func() {
		firstDone <- solveImageWithSharedSession(firstCtx, blockedClient, registry.ref, sharedSession)
	}()

	select {
	case <-credsProvider.credentialsStart:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for first solve to enter credentials fallback before blackholing session")
	}

	blackholeDialer.Activate()
	close(credsProvider.credentialsResume)

	secondDone := make(chan error, 1)
	secondStart := time.Now()
	go func() {
		secondDone <- solveImageWithAuth(sb.Context(), fastClient, registry.ref, fastProvider)
	}()

	select {
	case err := <-secondDone:
		require.Error(t, err)
		require.Less(t, time.Since(secondStart), 2*time.Second, "second solve should not be blocked by blackholed credentials fallback on another client")
	case <-time.After(2 * time.Second):
		t.Fatal("second solve blocked behind blackholed credentials fallback")
	}

	cleanupSession()
	select {
	case err := <-firstDone:
		require.Error(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("first solve did not finish after blackholed credentials fallback cleanup")
	}
}

func testAuthDeadlockBlackholedFetchTokenAuthority(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)

	registry := newAuthChallengeRegistry(t)

	blockedClient, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer blockedClient.Close()

	fastClient, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer fastClient.Close()

	blockedProvider := &authorityAuthProvider{
		host:         registry.host,
		privateKey:   newAuthorityPrivateKey(0x44),
		token:        registry.token,
		fetchStarted: make(chan struct{}),
		fetchResume:  make(chan struct{}),
	}
	fastProvider := &controlledAuthProvider{
		host:    registry.host,
		started: make(chan struct{}),
		release: make(chan struct{}),
	}

	blackholeDialer := newBlackholeSessionDialer(blockedClient.Dialer())
	sharedSession, cleanupSession := startSharedAuthSession(t, blockedProvider, blackholeDialer.Dial)

	firstCtx, cancelFirst := context.WithTimeoutCause(sb.Context(), 30*time.Second, context.DeadlineExceeded)
	defer cancelFirst()

	firstDone := make(chan error, 1)
	go func() {
		firstDone <- solveImageWithSharedSession(firstCtx, blockedClient, registry.ref, sharedSession)
	}()

	select {
	case <-blockedProvider.fetchStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for first solve to enter FetchToken authority callback before blackholing session")
	}

	blackholeDialer.Activate()
	close(blockedProvider.fetchResume)

	secondDone := make(chan error, 1)
	secondStart := time.Now()
	go func() {
		secondDone <- solveImageWithAuth(sb.Context(), fastClient, registry.ref, fastProvider)
	}()

	select {
	case err := <-secondDone:
		require.Error(t, err)
		require.Less(t, time.Since(secondStart), 2*time.Second, "second solve should not be blocked by blackholed FetchToken authority callback on another client")
	case <-time.After(2 * time.Second):
		t.Fatal("second solve blocked behind blackholed FetchToken authority callback")
	}

	cleanupSession()
	select {
	case err := <-firstDone:
		require.Error(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("first solve did not finish after blackholed FetchToken authority cleanup")
	}
}

func testAuthDeadlockVerifyTokenAuthorityReuse(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)

	registry := newAuthChallengeRegistry(t)

	seedClient, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer seedClient.Close()

	blockedClient, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer blockedClient.Close()

	fastClient, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer fastClient.Close()

	sharedKey := newAuthorityPrivateKey(0x55)
	seedProvider := &authorityAuthProvider{
		host:       registry.host,
		privateKey: sharedKey,
		token:      registry.token,
	}
	blockedProvider := &authorityAuthProvider{
		host:          registry.host,
		privateKey:    sharedKey,
		token:         registry.token,
		verifyStarted: make(chan struct{}),
		verifyResume:  make(chan struct{}),
	}
	fastProvider := &controlledAuthProvider{
		host:    registry.host,
		started: make(chan struct{}),
		release: make(chan struct{}),
	}

	seedErr := solveImageWithAuth(sb.Context(), seedClient, registry.ref, seedProvider)
	require.Error(t, seedErr)

	sharedSession, cleanupSession := startSharedAuthSession(t, blockedProvider, blockedClient.Dialer())

	firstCtx, cancelFirst := context.WithTimeoutCause(sb.Context(), 30*time.Second, context.DeadlineExceeded)
	defer cancelFirst()

	firstDone := make(chan error, 1)
	go func() {
		firstDone <- solveImageWithSharedSession(firstCtx, blockedClient, registry.ref, sharedSession)
	}()

	select {
	case <-blockedProvider.verifyStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for first solve to enter VerifyTokenAuthority reuse callback")
	}

	secondDone := make(chan error, 1)
	secondStart := time.Now()
	go func() {
		secondDone <- solveImageWithAuth(sb.Context(), fastClient, registry.ref, fastProvider)
	}()

	select {
	case err := <-secondDone:
		require.Error(t, err)
		require.Less(t, time.Since(secondStart), 2*time.Second, "second solve should not be blocked by unrelated VerifyTokenAuthority reuse callback")
	case <-time.After(2 * time.Second):
		t.Fatal("second solve blocked behind VerifyTokenAuthority reuse callback")
	}

	close(blockedProvider.verifyResume)
	cleanupSession()
	select {
	case err := <-firstDone:
		require.Error(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("first solve did not finish after VerifyTokenAuthority reuse cleanup")
	}
}
