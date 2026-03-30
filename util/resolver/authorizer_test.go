package resolver

import (
	"context"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/containerd/containerd/v2/core/remotes/docker/auth"
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

func TestAuthorizeDoesNotGloballyBlockOnSlowAuthFetch(t *testing.T) {
	sm, err := session.NewManager()
	require.NoError(t, err)

	handlerNS := newAuthHandlerNS(sm)
	blockingRT := &blockingRoundTripper{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}

	blockedHost := "blocked.example.com"
	blockedSession := "blocked-session"
	blockedClient := &http.Client{Transport: blockingRT}

	fastHost := "fast.example.com"
	fastSession := "fast-session"
	fastClient := &http.Client{}

	handlerNS.fetchers.withLock(func(state *fetcherState) {
		state.set(blockedHost, blockedSession, newAuthFetcher(blockedHost, blockedClient, auth.BearerAuth, nil, auth.TokenOptions{
			Realm:   "https://auth.blocked.example.com/token",
			Service: "registry.blocked",
			Scopes:  []string{"repository:foo/bar:pull"},
		}))
		state.set(fastHost, fastSession, newAuthFetcher(fastHost, fastClient, auth.BasicAuth, nil, auth.TokenOptions{
			Username: "u",
			Secret:   "s",
		}))
	})

	blockedAuthorizer := newDockerAuthorizer(blockedClient, handlerNS, sm, session.NewGroup(blockedSession))
	fastAuthorizer := newDockerAuthorizer(fastClient, handlerNS, sm, session.NewGroup(fastSession))

	blockedReq, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://"+blockedHost+"/v2/library/alpine/manifests/latest", nil)
	require.NoError(t, err)
	fastReq, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://"+fastHost+"/v2/library/alpine/manifests/latest", nil)
	require.NoError(t, err)

	blockedCtx, blockedCancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer blockedCancel()
	blockedDone := make(chan error, 1)
	go func() {
		blockedDone <- blockedAuthorizer.Authorize(blockedCtx, blockedReq)
	}()

	select {
	case <-blockingRT.started:
		// slow auth request has entered bearer token fetch.
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
		require.Less(t, time.Since(fastStart), 500*time.Millisecond, "fast authorize should not wait on unrelated slow auth request")
	case <-time.After(1 * time.Second):
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
