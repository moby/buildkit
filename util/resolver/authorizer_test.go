package resolver

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	dockerauth "github.com/containerd/containerd/v2/core/remotes/docker/auth"
	"github.com/moby/buildkit/session"
	"github.com/stretchr/testify/require"
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

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestAuthFetcherRetriesTransientTokenError(t *testing.T) {
	var attempts atomic.Int32
	client := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if attempts.Add(1) == 1 {
			return nil, syscall.ECONNRESET
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"token":"retry-token","expires_in":60}`)),
			Request:    req,
		}, nil
	})}
	opts := dockerauth.TokenOptions{
		Realm: "https://auth.example/token", Service: "registry.example",
		Scopes: []string{"repository:library/alpine:pull"}, Username: "user", Secret: "password",
	}
	fetcher := newAuthFetcher("registry.example", client, dockerauth.BearerAuth, nil, opts)

	res, err := fetcher.fetchToken(t.Context(), nil, nil, opts)
	require.NoError(t, err)
	require.Equal(t, "Bearer retry-token", res.token)
	require.Equal(t, int32(2), attempts.Load())
}

func TestAuthFetcherDoesNotRetryPermanentTokenError(t *testing.T) {
	var attempts atomic.Int32
	client := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		attempts.Add(1)
		return &http.Response{
			StatusCode: http.StatusForbidden,
			Header:     http.Header{"Content-Type": []string{"text/plain"}},
			Body:       io.NopCloser(strings.NewReader("forbidden")),
			Request:    req,
		}, nil
	})}
	opts := dockerauth.TokenOptions{
		Realm: "https://auth.example/token", Service: "registry.example",
		Scopes: []string{"repository:library/alpine:pull"}, Username: "user", Secret: "password",
	}
	fetcher := newAuthFetcher("registry.example", client, dockerauth.BearerAuth, nil, opts)

	res, err := fetcher.fetchToken(t.Context(), nil, nil, opts)
	require.Error(t, err)
	require.Nil(t, res)
	require.Equal(t, int32(1), attempts.Load())
}

func TestAuthFetcherPreservesGetToOAuthFallback(t *testing.T) {
	methods := make([]string, 0, 2)
	client := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		methods = append(methods, req.Method)
		if req.Method == http.MethodGet {
			return &http.Response{
				StatusCode: http.StatusUnauthorized,
				Header:     http.Header{"Content-Type": []string{"text/plain"}},
				Body:       io.NopCloser(strings.NewReader("unauthorized")),
				Request:    req,
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"access_token":"fallback-token","token_type":"Bearer","expires_in":60}`)),
			Request:    req,
		}, nil
	})}
	opts := dockerauth.TokenOptions{
		Realm: "https://auth.example/token", Service: "registry.example",
		Scopes: []string{"repository:library/alpine:pull"}, Username: "user", Secret: "password",
	}
	fetcher := newAuthFetcher("registry.example", client, dockerauth.BearerAuth, nil, opts)

	res, err := fetcher.fetchToken(t.Context(), nil, nil, opts)
	require.NoError(t, err)
	require.Equal(t, "Bearer fallback-token", res.token)
	require.Equal(t, []string{http.MethodGet, http.MethodPost}, methods)
}

func TestAuthFetcherRetriesTransientAnonymousTokenError(t *testing.T) {
	var attempts atomic.Int32
	client := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if attempts.Add(1) == 1 {
			return nil, io.EOF
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"token":"anonymous-token","expires_in":60}`)),
			Request:    req,
		}, nil
	})}
	opts := dockerauth.TokenOptions{
		Realm: "https://auth.example/token", Service: "registry.example",
		Scopes: []string{"repository:library/alpine:pull"},
	}
	fetcher := newAuthFetcher("registry.example", client, dockerauth.BearerAuth, nil, opts)

	res, err := fetcher.fetchToken(t.Context(), nil, nil, opts)
	require.NoError(t, err)
	require.Equal(t, "Bearer anonymous-token", res.token)
	require.Equal(t, int32(2), attempts.Load())
}
