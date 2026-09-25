package authprovider

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/docker/cli/cli/config/configfile"
	"github.com/docker/cli/cli/config/types"
	"github.com/moby/buildkit/session/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
