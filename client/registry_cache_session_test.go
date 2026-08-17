package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moby/buildkit/client/llb"
	gw "github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/session"
	sessionauth "github.com/moby/buildkit/session/auth"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/moby/buildkit/util/testutil/workers"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	"github.com/tonistiigi/fsutil"
	"google.golang.org/grpc"
)

// testRegistryCacheImportSessionRebind verifies that a lazy cache imported by
// session A uses session B for registry access after session A has closed.
func testRegistryCacheImportSessionRebind(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows", "test requires copying and merging states, but Windows layers support only one lower mount")
	workers.CheckFeatureCompat(t, sb,
		workers.FeatureCacheExport,
		workers.FeatureCacheImport,
		workers.FeatureCacheBackendRegistry,
		workers.FeatureMergeDiff,
	)

	ctx, cancel := context.WithTimeoutCause(sb.Context(), 2*time.Minute, errors.New("registry cache session rebind test timed out"))
	defer cancel()

	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	repo := "buildkit/session-cache-" + identity.NewID()
	authRegistry := newRegistryCacheAuthProxy(t, registry)
	backendRef := registry + "/" + repo + ":latest"
	cacheRef := authRegistry.host() + "/" + repo + ":latest"

	payload := []byte(strings.Repeat("registry cache session rebind\n", 32*1024))
	contextDir := t.TempDir()
	err = os.WriteFile(filepath.Join(contextDir, "payload"), payload, 0o644)
	require.NoError(t, err)
	localFS, err := fsutil.NewFS(contextDir)
	require.NoError(t, err)
	base := llb.Scratch().File(llb.Copy(llb.Local("context"), "payload", "/payload"))
	baseDef, err := base.Marshal(ctx)
	require.NoError(t, err)

	// Seed the cache through the unauthenticated backend. Only cache import is
	// routed through the auth proxy, keeping the setup independent of push auth.
	_, err = c.Solve(ctx, baseDef, SolveOpt{
		Exports: []ExportEntry{{Type: ExporterLocal, OutputDir: t.TempDir()}},
		LocalMounts: map[string]fsutil.FS{
			"context": localFS,
		},
		CacheExports: []CacheOptionsEntry{{
			Type: "registry",
			Attrs: map[string]string{
				"ref":  backendRef,
				"mode": "max",
			},
		}},
	}, nil)
	require.NoError(t, err)
	ensurePruneAll(t, c, sb)

	cacheAttrs := map[string]string{
		"ref":               cacheRef,
		"registry.insecure": "true",
	}

	// Build A imports the cache and exposes its lazy state while its session is active.
	stateFromA := make(chan llb.State, 1)
	releaseA := make(chan struct{})
	var releaseAOnce sync.Once
	t.Cleanup(func() { releaseAOnce.Do(func() { close(releaseA) }) })
	aDone := make(chan error, 1)

	go func() {
		_, err := c.Build(ctx, SolveOpt{
			LocalMounts: map[string]fsutil.FS{"context": localFS},
			Session:     []session.Attachable{registryCacheAuth(authRegistry.host(), "session-a")},
		}, "registry-cache-session-a", func(ctx context.Context, c gw.Client) (*gw.Result, error) {
			res, err := c.Solve(ctx, gw.SolveRequest{
				Evaluate:   true,
				Definition: baseDef.ToPB(),
				CacheImports: []gw.CacheOptionsEntry{{
					Type:  "registry",
					Attrs: cacheAttrs,
				}},
			})
			if err != nil {
				return nil, errors.Wrap(err, "build A imports registry cache")
			}
			ref, err := res.SingleRef()
			if err != nil {
				return nil, errors.Wrap(err, "build A gets result ref")
			}
			st, err := ref.ToState()
			if err != nil {
				return nil, errors.Wrap(err, "build A converts result to state")
			}
			select {
			case stateFromA <- st:
			case <-ctx.Done():
				return nil, errors.WithStack(context.Cause(ctx))
			}
			select {
			case <-releaseA:
				return gw.NewResult(), nil
			case <-ctx.Done():
				return nil, errors.WithStack(context.Cause(ctx))
			}
		}, nil)
		aDone <- err
	}()

	var stateA llb.State
	select {
	case stateA = <-stateFromA:
	case err := <-aDone:
		require.NoError(t, err)
		t.Fatal("build A ended before exposing its state")
	case <-ctx.Done():
		t.Fatal(context.Cause(ctx))
	}
	require.Positive(t, authRegistry.authorizedRequestsFor("session-a"))

	// Build B reuses A's state but must use its own session when materializing it.
	outputDir := t.TempDir()
	bDone := make(chan error, 1)
	go func() {
		_, err := c.Build(ctx, SolveOpt{
			Exports: []ExportEntry{{Type: ExporterLocal, OutputDir: outputDir}},
			Session: []session.Attachable{registryCacheAuth(authRegistry.host(), "session-b")},
		}, "registry-cache-session-b", func(ctx context.Context, c gw.Client) (*gw.Result, error) {
			delayed := llb.HTTP(authRegistry.url()+"/delay", llb.Filename("delay"), llb.IgnoreCache)
			merged := llb.Merge([]llb.State{stateA, delayed})
			out := llb.Scratch().File(llb.Copy(merged, "/payload", "/copied"))
			def, err := out.Marshal(ctx)
			if err != nil {
				return nil, errors.Wrap(err, "marshal build B definition")
			}
			return c.Solve(ctx, gw.SolveRequest{Evaluate: true, Definition: def.ToPB()})
		}, nil)
		bDone <- err
	}()

	// The delay request proves that build B has loaded the definition containing
	// A's state. Close A, revoke its credentials, and only then allow B to force the
	// lazy layer read. The fixed provider must authenticate that read through B.
	select {
	case <-authRegistry.delayStarted:
	case err := <-bDone:
		require.NoError(t, err)
		t.Fatal("build B ended before reaching the synchronization point")
	case <-ctx.Done():
		t.Fatal(context.Cause(ctx))
	}
	releaseAOnce.Do(func() { close(releaseA) })
	select {
	case err := <-aDone:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal(context.Cause(ctx))
	}
	authRegistry.disableSessionA()
	authRegistry.release()

	select {
	case err := <-bDone:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal(context.Cause(ctx))
	}
	require.Positive(t, authRegistry.authorizedRequestsFor("session-b"))
	dt, err := os.ReadFile(filepath.Join(outputDir, "copied"))
	require.NoError(t, err)
	require.Equal(t, payload, dt)
}

type registryCacheAuthProxy struct {
	server *httptest.Server
	proxy  *httputil.ReverseProxy

	mu                 sync.Mutex
	authorizedRequests map[string]int
	sessionAOn         bool

	delayStarted chan struct{}
	releaseDelay chan struct{}
	delayOnce    sync.Once
	releaseOnce  sync.Once
}

func newRegistryCacheAuthProxy(t *testing.T, backend string) *registryCacheAuthProxy {
	t.Helper()
	target, err := url.Parse("http://" + backend)
	require.NoError(t, err)

	p := &registryCacheAuthProxy{
		authorizedRequests: map[string]int{},
		sessionAOn:         true,
		delayStarted:       make(chan struct{}),
		releaseDelay:       make(chan struct{}),
	}
	p.proxy = &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Host = target.Host
			pr.Out.Header.Del("Authorization")
		},
	}
	p.server = httptest.NewServer(p)
	t.Cleanup(p.close)
	return p
}

func (p *registryCacheAuthProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/delay":
		p.delayOnce.Do(func() { close(p.delayStarted) })
		select {
		case <-p.releaseDelay:
			_, _ = w.Write([]byte("ready\n"))
		case <-r.Context().Done():
		}
		return
	case "/v2/":
		p.proxy.ServeHTTP(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v2/") {
		if !p.authorized(r) {
			w.Header().Set("WWW-Authenticate", `Basic realm="buildkit-test"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		p.proxy.ServeHTTP(w, r)
		return
	}
	http.NotFound(w, r)
}

func (p *registryCacheAuthProxy) authorized(r *http.Request) bool {
	username, password, ok := r.BasicAuth()
	if !ok || password != "password-"+username || (username != "session-a" && username != "session-b") {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if username == "session-a" && !p.sessionAOn {
		return false
	}
	p.authorizedRequests[username]++
	return true
}

func (p *registryCacheAuthProxy) disableSessionA() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sessionAOn = false
}

func (p *registryCacheAuthProxy) authorizedRequestsFor(username string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.authorizedRequests[username]
}

func (p *registryCacheAuthProxy) host() string {
	return strings.TrimPrefix(p.server.URL, "http://")
}

func (p *registryCacheAuthProxy) url() string {
	return p.server.URL
}

func (p *registryCacheAuthProxy) release() {
	p.releaseOnce.Do(func() { close(p.releaseDelay) })
}

func (p *registryCacheAuthProxy) close() {
	p.release()
	p.server.Close()
}

type registryCacheSessionAuth struct {
	sessionauth.UnimplementedAuthServer
	host     string
	username string
}

func registryCacheAuth(host, username string) session.Attachable {
	return &registryCacheSessionAuth{host: host, username: username}
}

func (p *registryCacheSessionAuth) Register(server *grpc.Server) {
	sessionauth.RegisterAuthServer(server, p)
}

func (p *registryCacheSessionAuth) Credentials(_ context.Context, req *sessionauth.CredentialsRequest) (*sessionauth.CredentialsResponse, error) {
	if req.Host != p.host {
		return nil, errors.Errorf("unexpected registry host %q", req.Host)
	}
	return &sessionauth.CredentialsResponse{
		Username: p.username,
		Secret:   "password-" + p.username,
	}, nil
}
