package authprovider

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/docker/cli/cli/config/configfile"
	"github.com/docker/cli/cli/config/types"
	"github.com/moby/buildkit/session/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFetchTokenRetry(t *testing.T) {
	for _, tc := range []struct {
		name      string
		anonymous bool
		statuses  []int // Zero resets the TCP connection without a response.
		methods   []string
	}{
		{"oauth reset", false, []int{0, http.StatusOK}, []string{http.MethodPost, http.MethodPost}},
		{"anonymous reset", true, []int{0, http.StatusOK}, []string{http.MethodGet, http.MethodGet}},
		{"fallback reset", false, []int{http.StatusUnauthorized, 0, http.StatusOK}, []string{http.MethodPost, http.MethodGet, http.MethodGet}},
		{"server error", false, []int{http.StatusServiceUnavailable, http.StatusOK}, []string{http.MethodPost, http.MethodPost}},
		{"forbidden", false, []int{http.StatusForbidden}, []string{http.MethodPost}},
		{"invalid credentials", false, []int{http.StatusUnauthorized, http.StatusUnauthorized}, []string{http.MethodPost, http.MethodGet}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var methods []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				methods = append(methods, r.Method)
				assert.NoError(t, r.ParseForm())
				assert.Equal(t, "registry.example", r.Form.Get("service"))
				assert.Equal(t, "repository:test:pull", r.Form.Get("scope"))
				if r.Method == http.MethodPost {
					assert.Equal(t, "password", r.Form.Get("grant_type"))
					assert.Equal(t, "user", r.Form.Get("username"))
					assert.Equal(t, "password", r.Form.Get("password"))
				} else if tc.anonymous {
					assert.Empty(t, r.Header.Get("Authorization"))
				} else {
					user, password, ok := r.BasicAuth()
					assert.True(t, ok)
					assert.Equal(t, "user", user)
					assert.Equal(t, "password", password)
				}
				// Do not let net/http transparently retry GET on a reused connection.
				w.Header().Set("Connection", "close")
				if len(methods) > len(tc.statuses) {
					t.Error("unexpected token request")
					w.WriteHeader(http.StatusForbidden)
					return
				}
				status := tc.statuses[len(methods)-1]
				if status == 0 {
					conn, _, err := w.(http.Hijacker).Hijack()
					if !assert.NoError(t, err) {
						return
					}
					assert.NoError(t, conn.(*net.TCPConn).SetLinger(0))
					assert.NoError(t, conn.Close())
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				if status == http.StatusOK {
					_, err := io.WriteString(w, `{"token":"retried","access_token":"retried","expires_in":120}`)
					assert.NoError(t, err)
				}
			}))
			defer srv.Close()
			p := NewDockerAuthProvider(DockerAuthProviderConfig{
				AuthConfigProvider: func(context.Context, string, []string, ExpireCachedAuthCheck) (types.AuthConfig, error) {
					if tc.anonymous {
						return types.AuthConfig{}, nil
					}
					return types.AuthConfig{Username: "user", Password: "password"}, nil
				},
			}).(*authProvider)
			res, err := p.FetchToken(t.Context(), &auth.FetchTokenRequest{
				Host: "registry.example", Realm: srv.URL, Service: "registry.example",
				Scopes: []string{"repository:test:pull"},
			})
			if tc.statuses[len(tc.statuses)-1] == http.StatusOK {
				require.NoError(t, err)
				require.Equal(t, "retried", res.Token)
				require.EqualValues(t, 120, res.ExpiresIn)
			} else {
				require.Error(t, err)
			}
			mu.Lock()
			defer mu.Unlock()
			require.Equal(t, tc.methods, methods)
		})
	}
}

func TestFetchTokenCaching(t *testing.T) {
	newCfg := func() *configfile.ConfigFile {
		return &configfile.ConfigFile{
			AuthConfigs: map[string]types.AuthConfig{
				DockerHubConfigfileKey: {Username: "user", RegistryToken: "hunter2"},
			},
		}
	}

	cfg := newCfg()
	p := NewDockerAuthProvider(DockerAuthProviderConfig{
		AuthConfigProvider: LoadAuthConfig(cfg),
	}).(*authProvider)
	res, err := p.FetchToken(t.Context(), &auth.FetchTokenRequest{Host: DockerHubRegistryHost})
	require.NoError(t, err)
	assert.Equal(t, "hunter2", res.Token)

	cfg.AuthConfigs[DockerHubConfigfileKey] = types.AuthConfig{Username: "user", RegistryToken: "hunter3"}
	res, err = p.FetchToken(t.Context(), &auth.FetchTokenRequest{Host: DockerHubRegistryHost})
	require.NoError(t, err)

	// Verify that we cached the result instead of returning hunter3.
	assert.Equal(t, "hunter2", res.Token)

	// Now again but this time expire the auth.

	cfg = newCfg()
	p = NewDockerAuthProvider(DockerAuthProviderConfig{
		AuthConfigProvider: LoadAuthConfig(cfg),
		ExpireCachedAuth: func(_ time.Time, host string) bool {
			require.Equal(t, DockerHubRegistryHost, host)
			return true
		},
	}).(*authProvider)

	res, err = p.FetchToken(t.Context(), &auth.FetchTokenRequest{Host: DockerHubRegistryHost})
	require.NoError(t, err)
	assert.Equal(t, "hunter2", res.Token)

	cfg.AuthConfigs[DockerHubConfigfileKey] = types.AuthConfig{Username: "user", RegistryToken: "hunter3"}
	res, err = p.FetchToken(t.Context(), &auth.FetchTokenRequest{Host: DockerHubRegistryHost})
	require.NoError(t, err)

	// Verify that we re-fetched the token after it expired.
	assert.Equal(t, "hunter3", res.Token)
}
