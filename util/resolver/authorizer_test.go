package resolver

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/containerd/containerd/v2/core/remotes/docker/auth"
	"github.com/moby/buildkit/session"
	"github.com/stretchr/testify/require"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
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

func TestFetchTokenRetriesOnTransientNetworkError(t *testing.T) {
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"token":      "retried-token",
			"expires_in": 60,
		}))
	}))
	defer tokenServer.Close()

	client := tokenServer.Client()
	realTransport := client.Transport

	var attempts atomic.Int32
	client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if attempts.Add(1) == 1 {
			// simulate the transient connection reset described in
			// https://github.com/moby/buildkit/issues/6981
			return nil, &net.OpError{Op: "read", Err: syscall.ECONNRESET}
		}
		return realTransport.RoundTrip(req)
	})

	ah := newAuthFetcher("registry.example", client, auth.BearerAuth, nil, auth.TokenOptions{
		Realm:   tokenServer.URL,
		Service: "registry.example",
	})

	sm, err := session.NewManager()
	require.NoError(t, err)

	token, err := ah.authorize(t.Context(), sm, session.NewGroup(""))
	require.NoError(t, err)
	require.Equal(t, "Bearer retried-token", token)
	require.EqualValues(t, 2, attempts.Load())
}
