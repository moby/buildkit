package resolver

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/containerd/containerd/v2/core/remotes/docker/auth"
	"github.com/moby/buildkit/session"
	sessionauth "github.com/moby/buildkit/session/auth"
	sessiontestutil "github.com/moby/buildkit/session/testutil"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
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

func TestAddResponsesDoesNotSerializeUnrelatedCredentialCallbacks(t *testing.T) {
	t.Parallel()

	const (
		slowHost = "slow.registry.test"
		fastHost = "fast.registry.test"
	)

	ctx := t.Context()
	sm, err := session.NewManager()
	require.NoError(t, err)

	slowStarted := make(chan struct{})
	slowRelease := make(chan struct{})
	var slowStartedOnce sync.Once
	var slowReleaseOnce sync.Once
	releaseSlow := func() {
		slowReleaseOnce.Do(func() {
			close(slowRelease)
		})
	}

	s, err := session.NewSession(ctx, "resolver-test")
	require.NoError(t, err)
	s.Allow(&testAuthProvider{
		credentials: func(ctx context.Context, req *sessionauth.CredentialsRequest) (*sessionauth.CredentialsResponse, error) {
			switch req.Host {
			case slowHost:
				slowStartedOnce.Do(func() {
					close(slowStarted)
				})
				<-slowRelease
				return &sessionauth.CredentialsResponse{
					Username: "slow-user",
					Secret:   "slow-secret",
				}, nil
			case fastHost:
				return &sessionauth.CredentialsResponse{
					Username: "fast-user",
					Secret:   "fast-secret",
				}, nil
			default:
				return nil, status.Errorf(codes.InvalidArgument, "unexpected host %q", req.Host)
			}
		},
	})

	dialer := session.Dialer(sessiontestutil.TestStream(sessiontestutil.Handler(sm.HandleConn)))
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		return s.Run(gctx, dialer)
	})
	defer func() {
		releaseSlow()
		require.NoError(t, s.Close())
		require.NoError(t, g.Wait())
	}()
	caller, err := sm.Get(ctx, s.ID(), false)
	require.NoError(t, err)
	require.NotNil(t, caller)

	a := newDockerAuthorizer(&http.Client{}, newAuthHandlerNS(sm), sm, session.NewGroup(s.ID()))

	slowDone := make(chan error, 1)
	go func() {
		header := http.Header{}
		header.Set("WWW-Authenticate", `Basic realm="test"`)
		resp := &http.Response{
			Header: header,
			Body:   io.NopCloser(strings.NewReader("")),
			Request: &http.Request{
				URL: &url.URL{
					Scheme: "https",
					Host:   slowHost,
				},
			},
		}
		defer resp.Body.Close()
		slowDone <- a.AddResponses(ctx, []*http.Response{resp})
	}()

	select {
	case <-slowStarted:
	case err := <-slowDone:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for slow credentials callback")
	}

	fastDone := make(chan error, 1)
	go func() {
		header := http.Header{}
		header.Set("WWW-Authenticate", `Basic realm="test"`)
		resp := &http.Response{
			Header: header,
			Body:   io.NopCloser(strings.NewReader("")),
			Request: &http.Request{
				URL: &url.URL{
					Scheme: "https",
					Host:   fastHost,
				},
			},
		}
		defer resp.Body.Close()
		fastDone <- a.AddResponses(ctx, []*http.Response{resp})
	}()

	select {
	case err := <-fastDone:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("unrelated auth challenge was blocked behind a slow credentials callback")
	}

	releaseSlow()
	require.NoError(t, <-slowDone)
}

func TestAuthorizeDoesNotSerializeUnrelatedRequests(t *testing.T) {
	t.Parallel()

	const (
		slowHost = "slow.registry.test"
		fastHost = "fast.registry.test"
	)

	ctx := t.Context()
	sm, err := session.NewManager()
	require.NoError(t, err)

	slowStarted := make(chan struct{})
	slowRelease := make(chan struct{})
	var slowStartedOnce sync.Once
	var slowReleaseOnce sync.Once
	releaseSlow := func() {
		slowReleaseOnce.Do(func() {
			close(slowRelease)
		})
	}

	s, err := session.NewSession(ctx, "resolver-test")
	require.NoError(t, err)
	s.Allow(&testAuthProvider{
		fetchToken: func(ctx context.Context, req *sessionauth.FetchTokenRequest) (*sessionauth.FetchTokenResponse, error) {
			if req.Host != slowHost {
				return nil, status.Errorf(codes.InvalidArgument, "unexpected host %q", req.Host)
			}
			slowStartedOnce.Do(func() {
				close(slowStarted)
			})
			<-slowRelease
			return &sessionauth.FetchTokenResponse{Token: "slow-token"}, nil
		},
	})

	dialer := session.Dialer(sessiontestutil.TestStream(sessiontestutil.Handler(sm.HandleConn)))
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		return s.Run(gctx, dialer)
	})
	defer func() {
		releaseSlow()
		require.NoError(t, s.Close())
		require.NoError(t, g.Wait())
	}()
	caller, err := sm.Get(ctx, s.ID(), false)
	require.NoError(t, err)
	require.NotNil(t, caller)

	handlerNS := newAuthHandlerNS(sm)
	handlerNS.set(slowHost, s.ID(), newAuthFetcher(slowHost, &http.Client{}, auth.BearerAuth, new([32]byte), auth.TokenOptions{
		Realm:   "https://auth.example.test/token",
		Service: "registry.test",
	}))
	handlerNS.set(fastHost, s.ID(), newAuthFetcher(fastHost, &http.Client{}, auth.BasicAuth, nil, auth.TokenOptions{
		Username: "fast-user",
		Secret:   "fast-secret",
	}))

	a := newDockerAuthorizer(&http.Client{}, handlerNS, sm, session.NewGroup(s.ID()))

	slowReq, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+slowHost+"/v2/", nil)
	require.NoError(t, err)
	slowDone := make(chan error, 1)
	go func() {
		slowDone <- a.Authorize(ctx, slowReq)
	}()

	select {
	case <-slowStarted:
	case err := <-slowDone:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for slow token callback")
	}

	fastReq, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+fastHost+"/v2/", nil)
	require.NoError(t, err)
	fastDone := make(chan error, 1)
	go func() {
		fastDone <- a.Authorize(ctx, fastReq)
	}()

	select {
	case err := <-fastDone:
		require.NoError(t, err)
		require.Equal(t, "Basic "+base64.StdEncoding.EncodeToString([]byte("fast-user:fast-secret")), fastReq.Header.Get("Authorization"))
	case <-time.After(2 * time.Second):
		t.Fatal("unrelated authorize request was blocked behind a slow token fetch")
	}

	releaseSlow()
	require.NoError(t, <-slowDone)
	require.Equal(t, "Bearer slow-token", slowReq.Header.Get("Authorization"))
}

type testAuthProvider struct {
	sessionauth.UnimplementedAuthServer

	credentials func(context.Context, *sessionauth.CredentialsRequest) (*sessionauth.CredentialsResponse, error)
	fetchToken  func(context.Context, *sessionauth.FetchTokenRequest) (*sessionauth.FetchTokenResponse, error)
}

func (p *testAuthProvider) Register(server *grpc.Server) {
	sessionauth.RegisterAuthServer(server, p)
}

func (p *testAuthProvider) Credentials(ctx context.Context, req *sessionauth.CredentialsRequest) (*sessionauth.CredentialsResponse, error) {
	if p.credentials == nil {
		return nil, status.Error(codes.Unimplemented, "credentials not implemented")
	}
	return p.credentials(ctx, req)
}

func (p *testAuthProvider) FetchToken(ctx context.Context, req *sessionauth.FetchTokenRequest) (*sessionauth.FetchTokenResponse, error) {
	if p.fetchToken == nil {
		return nil, status.Error(codes.Unimplemented, "fetch token not implemented")
	}
	return p.fetchToken(ctx, req)
}
