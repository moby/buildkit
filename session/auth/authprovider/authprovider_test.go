package authprovider

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	remoteserrors "github.com/containerd/containerd/v2/core/remotes/errors"
	"github.com/docker/cli/cli/config/configfile"
	"github.com/docker/cli/cli/config/types"
	"github.com/moby/buildkit/session/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFetchTokenRefreshRejectedCredentials(t *testing.T) {
	const host = "registry.example"
	expired := types.AuthConfig{Username: "user", Password: "expired"}
	fresh := types.AuthConfig{Username: "user", Password: "fresh"}
	refreshErr := errors.New("credential helper unavailable")
	const registryError = `{"errors":[{"code":"UNAUTHORIZED","message":"authentication failed"}]}`

	for _, tc := range []struct {
		name           string
		initial        types.AuthConfig
		replacement    types.AuthConfig
		postStatus     int
		getStatus      int
		acceptedSecret string
		errorBody      string
		refreshErr     error
		wantMethods    []string
		wantRefreshes  int
		wantStatus     int
		wantError      string
		wantExpires    int64
	}{
		{
			name:          "refresh before cache expiry",
			initial:       expired,
			replacement:   fresh,
			wantMethods:   []string{http.MethodPost, http.MethodGet, http.MethodPost},
			wantRefreshes: 1,
		},
		{
			name:          "preserve GET fallback after refresh",
			initial:       expired,
			replacement:   fresh,
			postStatus:    http.StatusMethodNotAllowed,
			wantMethods:   []string{http.MethodPost, http.MethodGet, http.MethodPost, http.MethodGet},
			wantRefreshes: 1,
		},
		{
			name:           "successful fallback needs no refresh",
			initial:        expired,
			replacement:    fresh,
			postStatus:     http.StatusUnauthorized,
			acceptedSecret: "expired",
			wantMethods:    []string{http.MethodPost, http.MethodGet},
		},
		{
			name:          "unchanged credentials are not retried",
			initial:       expired,
			replacement:   expired,
			wantMethods:   []string{http.MethodPost, http.MethodGet},
			wantRefreshes: 1,
			wantStatus:    http.StatusUnauthorized,
		},
		{
			name:          "replacement rejection stops retrying",
			initial:       expired,
			replacement:   fresh,
			postStatus:    http.StatusUnauthorized,
			getStatus:     http.StatusUnauthorized,
			wantMethods:   []string{http.MethodPost, http.MethodGet, http.MethodPost, http.MethodGet},
			wantRefreshes: 1,
			wantStatus:    http.StatusUnauthorized,
		},
		{
			name:          "refresh failure is returned",
			initial:       expired,
			replacement:   fresh,
			refreshErr:    refreshErr,
			wantMethods:   []string{http.MethodPost, http.MethodGet},
			wantRefreshes: 1,
		},
		{
			name:        "forbidden does not refresh credentials",
			initial:     expired,
			replacement: fresh,
			postStatus:  http.StatusForbidden,
			wantMethods: []string{http.MethodPost},
			wantStatus:  http.StatusForbidden,
		},
		{
			name:        "anonymous requests do not refresh credentials",
			initial:     types.AuthConfig{},
			replacement: fresh,
			wantMethods: []string{http.MethodGet},
			wantStatus:  http.StatusUnauthorized,
		},
		{
			name:          "refresh identity token",
			initial:       types.AuthConfig{IdentityToken: "expired"},
			replacement:   types.AuthConfig{IdentityToken: "fresh"},
			wantMethods:   []string{http.MethodPost, http.MethodGet, http.MethodPost},
			wantRefreshes: 1,
		},
		{
			name:          "refresh with structured registry error",
			initial:       expired,
			replacement:   fresh,
			errorBody:     registryError,
			wantMethods:   []string{http.MethodPost, http.MethodGet, http.MethodPost},
			wantRefreshes: 1,
		},
		{
			name:          "preserve structured registry error after retry",
			initial:       expired,
			replacement:   fresh,
			postStatus:    http.StatusUnauthorized,
			getStatus:     http.StatusUnauthorized,
			errorBody:     registryError,
			wantMethods:   []string{http.MethodPost, http.MethodGet, http.MethodPost, http.MethodGet},
			wantRefreshes: 1,
			wantError:     "failed to fetch oauth token: unauthorized: authentication failed",
		},
		{
			name:          "metadata changes do not retry credentials",
			initial:       expired,
			replacement:   types.AuthConfig{Username: "user", Password: "expired", ServerAddress: host},
			wantMethods:   []string{http.MethodPost, http.MethodGet},
			wantRefreshes: 1,
			wantStatus:    http.StatusUnauthorized,
		},
		{
			name:        "forbidden fallback does not refresh credentials",
			initial:     expired,
			replacement: fresh,
			getStatus:   http.StatusForbidden,
			wantMethods: []string{http.MethodPost, http.MethodGet},
			wantStatus:  http.StatusForbidden,
		},
		{
			name:          "replacement registry token needs no HTTP exchange",
			initial:       expired,
			replacement:   types.AuthConfig{RegistryToken: "registry-token"},
			wantMethods:   []string{http.MethodPost, http.MethodGet},
			wantRefreshes: 1,
			wantExpires:   defaultExpiration,
		},
		{
			name:          "empty replacement uses anonymous error context",
			initial:       expired,
			replacement:   types.AuthConfig{},
			errorBody:     "authentication failed",
			wantMethods:   []string{http.MethodPost, http.MethodGet, http.MethodGet},
			wantRefreshes: 1,
			wantStatus:    http.StatusUnauthorized,
			wantError:     "failed to fetch anonymous token:",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scopes := []string{"repository:test:pull,push"}
			cfg := configfile.New("")
			cfg.AuthConfigs[host] = tc.initial
			load := LoadAuthConfig(cfg)
			refreshes := 0
			var wantScopes []string
			p := NewDockerAuthProvider(DockerAuthProviderConfig{
				AuthConfigProvider: func(ctx context.Context, gotHost string, gotScopes []string, check ExpireCachedAuthCheck) (types.AuthConfig, error) {
					require.Equal(t, host, gotHost)
					require.Equal(t, wantScopes, gotScopes)
					if check(time.Now(), host) {
						refreshes++
						if tc.refreshErr != nil {
							return types.AuthConfig{}, tc.refreshErr
						}
					}
					return load(ctx, gotHost, gotScopes, check)
				},
			}).(*authProvider)
			// Cache a credential whose server-side lifetime ends before the cache TTL.
			_, err := p.Credentials(t.Context(), &auth.CredentialsRequest{Host: host})
			require.NoError(t, err)
			cfg.AuthConfigs[host] = tc.replacement
			wantScopes = scopes

			acceptedSecret := tc.acceptedSecret
			if acceptedSecret == "" {
				acceptedSecret = "fresh"
			}
			var methodsMu sync.Mutex
			var methods []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				methodsMu.Lock()
				methods = append(methods, r.Method)
				methodsMu.Unlock()
				if !assert.NoError(t, r.ParseForm()) {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				status := tc.postStatus
				secret := r.Form.Get("password")
				if r.Form.Get("grant_type") == "refresh_token" {
					secret = r.Form.Get("refresh_token")
				}
				if r.Method == http.MethodGet {
					status = tc.getStatus
					_, secret, _ = r.BasicAuth()
				}
				if status == 0 && secret != acceptedSecret {
					status = http.StatusUnauthorized
				}
				if status != 0 {
					w.WriteHeader(status)
					_, err := io.WriteString(w, tc.errorBody)
					assert.NoError(t, err)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, err := io.WriteString(w, `{"access_token":"registry-token","expires_in":120}`)
				assert.NoError(t, err)
			}))
			defer srv.Close()
			res, err := p.FetchToken(t.Context(), &auth.FetchTokenRequest{
				Host:    host,
				Realm:   srv.URL,
				Service: host,
				Scopes:  scopes,
			})
			switch {
			case tc.refreshErr != nil:
				require.ErrorIs(t, err, tc.refreshErr)
			case tc.wantStatus != 0:
				var statusErr remoteserrors.ErrUnexpectedStatus
				require.ErrorAs(t, err, &statusErr)
				require.Equal(t, tc.wantStatus, statusErr.StatusCode)
				if tc.wantError != "" {
					require.ErrorContains(t, err, tc.wantError)
				}
			case tc.wantError != "":
				require.EqualError(t, err, tc.wantError)
			default:
				require.NoError(t, err)
				require.Equal(t, "registry-token", res.Token)
				wantExpires := tc.wantExpires
				if wantExpires == 0 {
					wantExpires = 120
				}
				require.Equal(t, wantExpires, res.ExpiresIn)
			}
			if err != nil {
				require.Nil(t, res)
			}
			if tc.wantRefreshes != 0 && tc.refreshErr == nil {
				// A forced refresh must not replace the normal cache policy.
				wantScopes = nil
				creds, err := p.Credentials(t.Context(), &auth.CredentialsRequest{Host: host})
				require.NoError(t, err)
				require.Equal(t, toCredentials(tc.replacement), creds)
			}
			require.Equal(t, tc.wantRefreshes, refreshes)
			methodsMu.Lock()
			require.Equal(t, tc.wantMethods, methods)
			methodsMu.Unlock()
		})
	}
}

func TestFetchTokenProviderError(t *testing.T) {
	loadErr := errors.New("credential helper unavailable")
	calls := 0
	p := NewDockerAuthProvider(DockerAuthProviderConfig{
		AuthConfigProvider: func(context.Context, string, []string, ExpireCachedAuthCheck) (types.AuthConfig, error) {
			calls++
			return types.AuthConfig{}, loadErr
		},
	}).(*authProvider)
	res, err := p.FetchToken(t.Context(), &auth.FetchTokenRequest{Host: "registry.example"})
	// Loading credentials fails before an exchange, so preserve the provider's
	// error without token-exchange context or a forced-refresh attempt.
	require.Same(t, loadErr, err)
	require.Nil(t, res)
	require.Equal(t, 1, calls)
}

func TestFetchTokenRetry(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		if attempts.Add(1) == 1 {
			conn, _, err := w.(http.Hijacker).Hijack()
			if !assert.NoError(t, err) {
				return
			}
			assert.NoError(t, conn.(*net.TCPConn).SetLinger(0))
			assert.NoError(t, conn.Close())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, err := io.WriteString(w, `{"access_token":"retried","expires_in":120}`)
		assert.NoError(t, err)
	}))
	defer srv.Close()

	p := NewDockerAuthProvider(DockerAuthProviderConfig{
		AuthConfigProvider: func(context.Context, string, []string, ExpireCachedAuthCheck) (types.AuthConfig, error) {
			return types.AuthConfig{Username: "user", Password: "password"}, nil
		},
	}).(*authProvider)
	res, err := p.FetchToken(t.Context(), &auth.FetchTokenRequest{
		Host: "registry.example", Realm: srv.URL, Service: "registry.example",
		Scopes: []string{"repository:test:pull"},
	})
	require.NoError(t, err)
	require.Equal(t, "retried", res.Token)
	require.EqualValues(t, 120, res.ExpiresIn)
	require.EqualValues(t, 2, attempts.Load())
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
