package resolver

import (
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

	"github.com/containerd/containerd/v2/core/remotes/docker"
	"github.com/moby/buildkit/session"
	"github.com/stretchr/testify/require"
)

type tokenRoundTripper func(*http.Request) (*http.Response, error)

func (f tokenRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestResolveTokenEOFRetryBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		registryRequests, tokenRequests := 0, 0
		client := &http.Client{Transport: tokenRoundTripper(func(req *http.Request) (*http.Response, error) {
			if req.URL.Path == "/token" {
				tokenRequests++
				return nil, io.EOF
			}
			registryRequests++
			return &http.Response{
				StatusCode: http.StatusUnauthorized,
				Header: http.Header{"Www-Authenticate": {
					`Bearer realm="https://registry.example/token",service="registry.example"`,
				}},
				Body:    io.NopCloser(strings.NewReader("")),
				Request: req,
			}, nil
		})}
		authorizer := newDockerAuthorizer(client, newAuthHandlerNS(nil), nil, nil)
		resolver := docker.NewResolver(docker.ResolverOptions{Hosts: func(string) ([]docker.RegistryHost, error) {
			return []docker.RegistryHost{{
				Client: client, Authorizer: authorizer, Host: "registry.example", Scheme: "https", Path: "/v2",
				Capabilities: docker.HostCapabilityResolve | docker.HostCapabilityPull,
			}}, nil
		}})
		_, _, err := resolver.Resolve(t.Context(), "registry.example/test:latest")
		require.ErrorIs(t, err, io.EOF)
		require.Equal(t, 1, registryRequests)
		require.Equal(t, 4, tokenRequests)
	})
}

func TestResolveTokenServerErrorRetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tokenRequests := 0
		authorized := false
		client := &http.Client{Transport: tokenRoundTripper(func(req *http.Request) (*http.Response, error) {
			status := http.StatusUnauthorized
			body := ""
			header := http.Header{}
			if req.URL.Path == "/token" {
				tokenRequests++
				status = http.StatusServiceUnavailable
				if tokenRequests == 2 {
					status, body = http.StatusOK, `{"token":"retried","expires_in":120}`
				}
			} else if req.Header.Get("Authorization") != "" {
				authorized = req.Header.Get("Authorization") == "Bearer retried"
				status = http.StatusNotFound
			} else {
				header.Set("WWW-Authenticate", `Bearer realm="https://registry.example/token",service="registry.example"`)
			}
			return &http.Response{
				StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body)), Request: req,
			}, nil
		})}
		authorizer := newDockerAuthorizer(client, newAuthHandlerNS(nil), nil, nil)
		resolver := docker.NewResolver(docker.ResolverOptions{Hosts: func(string) ([]docker.RegistryHost, error) {
			return []docker.RegistryHost{{
				Client: client, Authorizer: authorizer, Host: "registry.example", Scheme: "https", Path: "/v2",
				Capabilities: docker.HostCapabilityResolve | docker.HostCapabilityPull,
			}}, nil
		}})
		_, _, err := resolver.Resolve(t.Context(), "registry.example/test:latest")
		require.Error(t, err)
		require.Equal(t, 2, tokenRequests)
		require.True(t, authorized)
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
