package registry

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/remotes/docker"
	"github.com/docker/cli/cli/config/configfile"
	"github.com/docker/cli/cli/config/types"
	cacheimport "github.com/moby/buildkit/cache/remotecache/v1"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/auth/authprovider"
	"github.com/moby/buildkit/util/contentutil"
	"github.com/moby/buildkit/util/resolver"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

func TestRegistryCacheProviderUsesCurrentSession(t *testing.T) {
	layer := []byte("a lazily fetched cache layer")
	desc := ocispecs.Descriptor{Digest: digest.FromBytes(layer), Size: int64(len(layer))}

	var tokenURL string
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			username, password, ok := r.BasicAuth()
			if !ok || username != "session-b" || password != "password-b" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = io.WriteString(w, `{"token":"token-b","expires_in":300}`)
		case "/v2/":
			w.WriteHeader(http.StatusOK)
		case "/v2/test/blobs/" + desc.Digest.String():
			if r.Header.Get("Authorization") != "Bearer token-b" {
				w.Header().Set("WWW-Authenticate", `Bearer realm="`+tokenURL+`",service="repro",scope="repository:test:pull"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write(layer)
		default:
			http.NotFound(w, r)
		}
	}))
	defer registry.Close()
	tokenURL = registry.URL + "/token"

	u, err := url.Parse(registry.URL)
	require.NoError(t, err)
	hosts := func(string) ([]docker.RegistryHost, error) {
		return []docker.RegistryHost{{
			Client:       registry.Client(),
			Host:         u.Host,
			Scheme:       u.Scheme,
			Path:         "/v2",
			Capabilities: docker.HostCapabilityPull | docker.HostCapabilityResolve,
		}}, nil
	}

	sm, err := session.NewManager()
	require.NoError(t, err)
	sessionA := startAuthSession(t, sm, u.Host, "session-a", "password-a")
	ref := u.Host + "/test:latest"
	provider := &registryCacheProvider{
		resolver: resolver.NewPool().GetResolver(hosts, ref, resolver.ScopeType{}, sm, session.NewGroup(sessionA.ID())),
		ref:      ref,
		xref:     ref,
	}
	pair := cacheimport.DescriptorProviderPair{Descriptor: desc, Provider: provider}
	multiProvider := contentutil.NewMultiProvider(pair)
	multiProvider.Add(desc.Digest, pair)

	oldSessionID := sessionA.ID()
	require.NoError(t, sessionA.Close())
	waitForSessionRemoval(t, sm, oldSessionID)

	sessionB := startAuthSession(t, sm, u.Host, "session-b", "password-b")
	defer sessionB.Close()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()

	currentProvider := contentutil.ProviderForSession(multiProvider, session.NewGroup(sessionB.ID()))
	got, err := content.ReadBlob(ctx, currentProvider, desc)
	require.NoError(t, err)
	require.Equal(t, layer, got)
}

func startAuthSession(t *testing.T, sm *session.Manager, host, username, password string) *session.Session {
	t.Helper()
	s, err := session.NewSession(t.Context(), "registry-cache-session-test")
	require.NoError(t, err)
	s.Allow(authprovider.NewDockerAuthProvider(authprovider.DockerAuthProviderConfig{
		AuthConfigProvider: authprovider.LoadAuthConfig(&configfile.ConfigFile{AuthConfigs: map[string]types.AuthConfig{
			host: {Username: username, Password: password},
		}}),
	}))
	go func() {
		_ = s.Run(t.Context(), func(ctx context.Context, _ string, meta map[string][]string) (net.Conn, error) {
			client, server := net.Pipe()
			go func() {
				_ = sm.HandleConn(ctx, server, meta)
				_ = server.Close()
			}()
			return client, nil
		})
	}()

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	_, err = sm.Get(ctx, s.ID(), false)
	require.NoError(t, err)
	return s
}

func waitForSessionRemoval(t *testing.T, sm *session.Manager, id string) {
	t.Helper()
	require.Eventually(t, func() bool {
		caller, err := sm.Get(t.Context(), id, true)
		return err == nil && caller == nil
	}, time.Second, 10*time.Millisecond)
}
