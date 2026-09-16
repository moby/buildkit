package resolver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	dockerauth "github.com/containerd/containerd/v2/core/remotes/docker/auth"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/util/resolver/retryhandler"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
)

type tokenRoundTripper func(*http.Request) (*http.Response, error)

func (f tokenRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestAuthFetcherRetry(t *testing.T) {
	for _, tc := range []struct {
		name      string
		anonymous bool
		nested    bool
		statuses  []int // Zero simulates a dropped connection.
		methods   []string
	}{
		{"get eof", false, false, []int{0, http.StatusOK}, []string{http.MethodGet, http.MethodGet}},
		{"anonymous eof", true, false, []int{0, http.StatusOK}, []string{http.MethodGet, http.MethodGet}},
		{"fallback eof", false, false, []int{http.StatusUnauthorized, 0, http.StatusOK}, []string{http.MethodGet, http.MethodPost, http.MethodPost}},
		{"server error", false, false, []int{http.StatusServiceUnavailable, http.StatusOK}, []string{http.MethodGet, http.MethodGet}},
		{"forbidden", false, false, []int{http.StatusForbidden}, []string{http.MethodGet}},
		{"nested exhausted", false, true, []int{0, 0, 0, 0}, []string{http.MethodGet, http.MethodGet, http.MethodGet, http.MethodGet}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var methods []string
				client := &http.Client{Transport: tokenRoundTripper(func(req *http.Request) (*http.Response, error) {
					methods = append(methods, req.Method)
					if len(methods) > len(tc.statuses) {
						t.Error("unexpected token request")
						return nil, errors.New("unexpected token request")
					}
					status := tc.statuses[len(methods)-1]
					if status == 0 {
						return nil, io.EOF
					}
					return &http.Response{
						StatusCode: status,
						Header:     http.Header{"Content-Type": {"application/json"}},
						Body:       io.NopCloser(strings.NewReader(`{"token":"retried","access_token":"retried","expires_in":120}`)),
						Request:    req,
					}, nil
				})}
				opts := dockerauth.TokenOptions{Realm: "https://registry.example/token", Service: "registry.example"}
				if !tc.anonymous {
					opts.Username, opts.Secret = "user", "password"
				}
				fetcher := newAuthFetcher("registry.example", client, dockerauth.BearerAuth, nil, opts)
				var token string
				fetch := func(ctx context.Context, _ ocispecs.Descriptor) ([]ocispecs.Descriptor, error) {
					var err error
					token, err = fetcher.doBearerAuth(ctx, nil, nil)
					return nil, err
				}
				if tc.nested {
					fetch = retryhandler.New(fetch, nil)
				}
				start := time.Now()
				_, err := fetch(t.Context(), ocispecs.Descriptor{})
				if tc.statuses[len(tc.statuses)-1] == http.StatusOK {
					require.NoError(t, err)
					require.Equal(t, "Bearer retried", token)
				} else {
					require.Error(t, err)
				}
				if tc.nested {
					require.ErrorIs(t, err, io.EOF)
					require.Equal(t, 7*time.Second, time.Since(start))
				}
				require.Equal(t, tc.methods, methods)
			})
		})
	}
}

func TestAuthorizeCancellationDuringTokenBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		attempts := 0
		client := &http.Client{Transport: tokenRoundTripper(func(req *http.Request) (*http.Response, error) {
			attempts++
			if attempts == 1 {
				return nil, io.EOF
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{},
				Body:       io.NopCloser(strings.NewReader(`{"token":"recovered","expires_in":120}`)),
				Request:    req,
			}, nil
		})}
		ns := newAuthHandlerNS(nil)
		ns.set("registry.example", "", newAuthFetcher("registry.example", client, dockerauth.BearerAuth, nil, dockerauth.TokenOptions{
			Realm: "https://registry.example/token",
		}))
		authorizer := newDockerAuthorizer(client, ns, nil, nil)
		ctx, cancel := context.WithCancelCause(t.Context())
		defer cancel(context.Canceled)
		req := httptest.NewRequestWithContext(ctx, http.MethodGet, "https://registry.example/v2/test/manifests/latest", nil)
		done := make(chan error, 1)
		go func() { done <- authorizer.Authorize(ctx, req) }()
		synctest.Wait()
		start := time.Now()
		cancel(context.Canceled)
		require.Error(t, <-done)
		require.Equal(t, 1, attempts)
		require.Zero(t, time.Since(start))

		// Cancellation must release the namespace lock and the flightcontrol call
		// so that another request can fetch and cache a token.
		req = httptest.NewRequestWithContext(t.Context(), http.MethodGet, req.URL.String(), nil)
		require.NoError(t, authorizer.Authorize(t.Context(), req))
		require.Equal(t, "Bearer recovered", req.Header.Get("Authorization"))
		require.NoError(t, authorizer.Authorize(t.Context(), req))
		require.Equal(t, 2, attempts)
	})
}

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

func TestBearerAuthFallsBackToAnonymousTokenWithoutSession(t *testing.T) {
	type tokenRequest struct {
		authorization string
		service       string
		scope         string
	}
	tokenRequests := make(chan tokenRequest, 1)
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenRequests <- tokenRequest{
			authorization: r.Header.Get("Authorization"),
			service:       r.URL.Query().Get("service"),
			scope:         r.URL.Query().Get("scope"),
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"token":      "anonymous-token",
			"expires_in": 60,
		}); err != nil {
			t.Errorf("failed to write token response: %v", err)
		}
	}))
	defer tokenServer.Close()

	sm, err := session.NewManager()
	require.NoError(t, err)

	auth := newDockerAuthorizer(tokenServer.Client(), newAuthHandlerNS(sm), sm, session.NewGroup(""))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "https://registry.example/v2/library/alpine/manifests/latest", nil)
	res := &http.Response{
		StatusCode: http.StatusUnauthorized,
		Header:     http.Header{},
		Request:    req,
	}
	res.Header.Set("WWW-Authenticate", fmt.Sprintf(
		`Bearer realm=%q,service="registry.example",scope="repository:library/alpine:pull"`,
		tokenServer.URL+"/token",
	))

	require.NoError(t, auth.AddResponses(t.Context(), []*http.Response{res}))

	retryReq := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "https://registry.example/v2/library/alpine/manifests/latest", nil)
	require.NoError(t, auth.Authorize(t.Context(), retryReq))
	require.Equal(t, "Bearer anonymous-token", retryReq.Header.Get("Authorization"))

	select {
	case req := <-tokenRequests:
		require.Empty(t, req.authorization)
		require.Equal(t, "registry.example", req.service)
		require.Equal(t, "repository:library/alpine:pull", req.scope)
	case <-time.After(time.Second):
		t.Fatal("expected anonymous token request")
	}
}
