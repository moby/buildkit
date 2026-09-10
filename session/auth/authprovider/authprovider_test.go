package authprovider

import (
	"context"
	"encoding/json"
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

func TestFetchTokenRetriesTransientServerError(t *testing.T) {
	var attempts atomic.Int32
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) == 1 {
			http.Error(w, "temporary failure", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"access_token": "retry-token",
			"token_type":   "Bearer",
			"expires_in":   60,
		}); err != nil {
			t.Errorf("failed to write token response: %v", err)
		}
	}))
	defer tokenServer.Close()

	p := NewDockerAuthProvider(DockerAuthProviderConfig{
		AuthConfigProvider: func(context.Context, string, []string, ExpireCachedAuthCheck) (types.AuthConfig, error) {
			return types.AuthConfig{Username: "user", Password: "password"}, nil
		},
	}).(*authProvider)

	res, err := p.FetchToken(t.Context(), &auth.FetchTokenRequest{
		Host: "registry.example", Realm: tokenServer.URL, Service: "registry.example",
		Scopes: []string{"repository:library/alpine:pull"},
	})
	require.NoError(t, err)
	require.Equal(t, "retry-token", res.Token)
	require.Equal(t, int32(2), attempts.Load())
}

func TestFetchTokenDoesNotRetryPermanentServerError(t *testing.T) {
	var attempts atomic.Int32
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer tokenServer.Close()

	p := NewDockerAuthProvider(DockerAuthProviderConfig{
		AuthConfigProvider: func(context.Context, string, []string, ExpireCachedAuthCheck) (types.AuthConfig, error) {
			return types.AuthConfig{Username: "user", Password: "password"}, nil
		},
	}).(*authProvider)

	_, err := p.FetchToken(t.Context(), &auth.FetchTokenRequest{
		Host: "registry.example", Realm: tokenServer.URL, Service: "registry.example",
		Scopes: []string{"repository:library/alpine:pull"},
	})
	require.Error(t, err)
	require.Equal(t, int32(1), attempts.Load())
}

func TestFetchTokenPreservesOAuthToGetFallback(t *testing.T) {
	methods := make([]string, 0, 2)
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		if r.Method == http.MethodPost {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"token":      "fallback-token",
			"expires_in": 60,
		}); err != nil {
			t.Errorf("failed to write token response: %v", err)
		}
	}))
	defer tokenServer.Close()

	p := NewDockerAuthProvider(DockerAuthProviderConfig{
		AuthConfigProvider: func(context.Context, string, []string, ExpireCachedAuthCheck) (types.AuthConfig, error) {
			return types.AuthConfig{Username: "user", Password: "password"}, nil
		},
	}).(*authProvider)

	res, err := p.FetchToken(t.Context(), &auth.FetchTokenRequest{
		Host: "registry.example", Realm: tokenServer.URL, Service: "registry.example",
		Scopes: []string{"repository:library/alpine:pull"},
	})
	require.NoError(t, err)
	require.Equal(t, "fallback-token", res.Token)
	require.Equal(t, []string{http.MethodPost, http.MethodGet}, methods)
}

func TestFetchTokenRetriesTransientAnonymousServerError(t *testing.T) {
	var attempts atomic.Int32
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) == 1 {
			http.Error(w, "temporary failure", http.StatusServiceUnavailable)
			return
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

	p := NewDockerAuthProvider(DockerAuthProviderConfig{
		AuthConfigProvider: func(context.Context, string, []string, ExpireCachedAuthCheck) (types.AuthConfig, error) {
			return types.AuthConfig{}, nil
		},
	}).(*authProvider)

	res, err := p.FetchToken(t.Context(), &auth.FetchTokenRequest{
		Host: "registry.example", Realm: tokenServer.URL, Service: "registry.example",
		Scopes: []string{"repository:library/alpine:pull"},
	})
	require.NoError(t, err)
	require.Equal(t, "anonymous-token", res.Token)
	require.Equal(t, int32(2), attempts.Load())
}
