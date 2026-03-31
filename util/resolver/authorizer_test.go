package resolver

import (
	"context"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	dockerauth "github.com/containerd/containerd/v2/core/remotes/docker/auth"
	"github.com/moby/buildkit/session"
	sessionauth "github.com/moby/buildkit/session/auth"
	"github.com/moby/buildkit/session/testutil"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestParseScopes(t *testing.T) {
	for _, tc := range []struct {
		name     string
		input    []string
		expected scopes
	}{
		{
			name:     "InvalidScope",
			input:    []string{""},
			expected: nil,
		},
		{
			name: "SeparateStrings",
			input: []string{
				"repository:foo/bar:pull",
				"repository:foo/baz:pull,push",
			},
			expected: map[string]map[string]struct{}{
				"repository:foo/bar": {
					"pull": struct{}{},
				},
				"repository:foo/baz": {
					"pull": struct{}{},
					"push": struct{}{},
				},
			},
		},
		{
			name:  "CombinedStrings",
			input: []string{"repository:foo/bar:pull repository:foo/baz:pull,push"},
			expected: map[string]map[string]struct{}{
				"repository:foo/bar": {
					"pull": struct{}{},
				},
				"repository:foo/baz": {
					"pull": struct{}{},
					"push": struct{}{},
				},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parsed := parseScopes(tc.input)
			if !reflect.DeepEqual(parsed, tc.expected) {
				t.Fatalf("expected %v, got %v", tc.expected, parsed)
			}
		})
	}
}

type blockingRoundTripper struct {
	started chan struct{}
	release chan struct{}
}

func (rt *blockingRoundTripper) RoundTrip(_ *http.Request) (*http.Response, error) {
	select {
	case <-rt.started:
	default:
		close(rt.started)
	}

	<-rt.release
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"token":"token-value","expires_in":60}`)),
	}, nil
}

type testAuthServer struct {
	sessionauth.UnimplementedAuthServer

	blockedHost string
	started     chan struct{}
	release     chan struct{}
	credentials map[string]*sessionauth.CredentialsResponse
}

func (s *testAuthServer) Register(server *grpc.Server) {
	sessionauth.RegisterAuthServer(server, s)
}

func (s *testAuthServer) Credentials(_ context.Context, req *sessionauth.CredentialsRequest) (*sessionauth.CredentialsResponse, error) {
	if resp, ok := s.credentials[req.Host]; ok {
		return resp, nil
	}
	return &sessionauth.CredentialsResponse{}, nil
}

func (s *testAuthServer) GetTokenAuthority(_ context.Context, req *sessionauth.GetTokenAuthorityRequest) (*sessionauth.GetTokenAuthorityResponse, error) {
	if req.Host == s.blockedHost {
		select {
		case <-s.started:
		default:
			close(s.started)
		}
		<-s.release
	}
	return nil, status.Error(codes.Unimplemented, "not implemented in test")
}

func startResolverAuthSession(t *testing.T, sm *session.Manager, provider session.Attachable) (*session.Session, func()) {
	t.Helper()

	s, err := session.NewSession(t.Context(), "resolver-test")
	require.NoError(t, err)
	s.Allow(provider)

	runCtx, runCancel := context.WithCancelCause(t.Context())
	runErrCh := make(chan error, 1)
	dialer := session.Dialer(testutil.TestStream(testutil.Handler(sm.HandleConn)))

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

func TestAuthorizeDoesNotGloballyBlockOnSlowAuthFetch(t *testing.T) {
	sm, err := session.NewManager()
	require.NoError(t, err)

	handlerNS := newAuthHandlerNS(sm)
	blockingRT := &blockingRoundTripper{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	defer func() {
		select {
		case <-blockingRT.release:
		default:
			close(blockingRT.release)
		}
	}()

	blockedHost := "blocked.example.com"
	blockedSession := "blocked-session"
	blockedClient := &http.Client{Transport: blockingRT}

	fastHost := "fast.example.com"
	fastSession := "fast-session"
	fastClient := &http.Client{}

	handlerNS.set(blockedHost, blockedSession, newAuthFetcher(blockedHost, blockedClient, dockerauth.BearerAuth, nil, dockerauth.TokenOptions{
		Realm:   "https://auth.blocked.example.com/token",
		Service: "registry.blocked",
		Scopes:  []string{"repository:foo/bar:pull"},
	}))
	handlerNS.set(fastHost, fastSession, newAuthFetcher(fastHost, fastClient, dockerauth.BasicAuth, nil, dockerauth.TokenOptions{
		Username: "u",
		Secret:   "s",
	}))

	blockedAuthorizer := newDockerAuthorizer(blockedClient, handlerNS, sm, session.NewGroup(blockedSession))
	fastAuthorizer := newDockerAuthorizer(fastClient, handlerNS, sm, session.NewGroup(fastSession))

	blockedReq, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://"+blockedHost+"/v2/library/alpine/manifests/latest", nil)
	require.NoError(t, err)
	fastReq, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://"+fastHost+"/v2/library/alpine/manifests/latest", nil)
	require.NoError(t, err)

	blockedCtx, blockedCancel := context.WithTimeoutCause(t.Context(), 10*time.Second, context.DeadlineExceeded)
	defer blockedCancel()

	blockedDone := make(chan error, 1)
	go func() {
		blockedDone <- blockedAuthorizer.Authorize(blockedCtx, blockedReq)
	}()

	select {
	case <-blockingRT.started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for blocked authorize request to start token fetch")
	}

	fastDone := make(chan error, 1)
	fastStart := time.Now()
	go func() {
		fastDone <- fastAuthorizer.Authorize(t.Context(), fastReq)
	}()

	select {
	case err := <-fastDone:
		require.NoError(t, err)
		require.Equal(t, "Basic dTpz", fastReq.Header.Get("Authorization"))
		require.Less(t, time.Since(fastStart), time.Second, "fast authorize should not wait on unrelated slow auth request")
	case <-time.After(time.Second):
		t.Fatal("fast authorize blocked behind slow auth request")
	}

	close(blockingRT.release)
	select {
	case err := <-blockedDone:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("blocked authorize did not finish after releasing transport")
	}
}

func TestAddResponsesDoesNotGloballyBlockOnSlowAuthCallback(t *testing.T) {
	sm, err := session.NewManager()
	require.NoError(t, err)

	handlerNS := newAuthHandlerNS(sm)

	blockedHost := "blocked.example.com"
	fastHost := "fast.example.com"
	blockedProvider := &testAuthServer{
		blockedHost: blockedHost,
		started:     make(chan struct{}),
		release:     make(chan struct{}),
		credentials: map[string]*sessionauth.CredentialsResponse{
			blockedHost: {
				Username: "blocked-user",
				Secret:   "blocked-secret",
			},
		},
	}
	defer func() {
		select {
		case <-blockedProvider.release:
		default:
			close(blockedProvider.release)
		}
	}()

	fastProvider := &testAuthServer{
		credentials: map[string]*sessionauth.CredentialsResponse{
			fastHost: {
				Username: "fast-user",
				Secret:   "fast-secret",
			},
		},
	}

	blockedSession, cleanupBlocked := startResolverAuthSession(t, sm, blockedProvider)
	defer cleanupBlocked()
	fastSession, cleanupFast := startResolverAuthSession(t, sm, fastProvider)
	defer cleanupFast()

	blockedAuthorizer := newDockerAuthorizer(&http.Client{}, handlerNS, sm, session.NewGroup(blockedSession.ID()))
	fastAuthorizer := newDockerAuthorizer(&http.Client{}, handlerNS, sm, session.NewGroup(fastSession.ID()))

	blockedReq, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://"+blockedHost+"/v2/library/alpine/manifests/latest", nil)
	require.NoError(t, err)
	fastReq, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://"+fastHost+"/v2/library/alpine/manifests/latest", nil)
	require.NoError(t, err)

	blockedResponses := []*http.Response{{
		StatusCode: http.StatusUnauthorized,
		Header: http.Header{
			"Www-Authenticate": []string{`Bearer realm="https://auth.blocked.example.com/token",service="registry.blocked",scope="repository:foo/bar:pull"`},
		},
		Request: blockedReq,
	}}
	fastResponses := []*http.Response{{
		StatusCode: http.StatusUnauthorized,
		Header: http.Header{
			"Www-Authenticate": []string{`Basic realm="registry.fast.example.com"`},
		},
		Request: fastReq,
	}}

	blockedCtx, blockedCancel := context.WithTimeoutCause(t.Context(), 10*time.Second, context.DeadlineExceeded)
	defer blockedCancel()

	blockedDone := make(chan error, 1)
	go func() {
		blockedDone <- blockedAuthorizer.AddResponses(blockedCtx, blockedResponses)
	}()

	select {
	case <-blockedProvider.started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for blocked AddResponses request to enter auth callback")
	}

	fastDone := make(chan error, 1)
	fastStart := time.Now()
	go func() {
		fastDone <- fastAuthorizer.AddResponses(t.Context(), fastResponses)
	}()

	select {
	case err := <-fastDone:
		require.NoError(t, err)
		handler := handlerNS.get(t.Context(), fastHost, sm, session.NewGroup(fastSession.ID()))
		require.NotNil(t, handler)
		require.Equal(t, "fast-user", handler.common.Username)
		require.Equal(t, "fast-secret", handler.common.Secret)
		require.Less(t, time.Since(fastStart), time.Second, "fast AddResponses should not wait on unrelated slow auth callback")
	case <-time.After(time.Second):
		t.Fatal("fast AddResponses blocked behind slow auth callback")
	}

	close(blockedProvider.release)
	select {
	case err := <-blockedDone:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("blocked AddResponses did not finish after releasing auth callback")
	}
}
