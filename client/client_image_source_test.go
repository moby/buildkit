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
	"github.com/moby/buildkit/client/llb/sourceresolver"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	"github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/session"
	sessionauth "github.com/moby/buildkit/session/auth"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/moby/buildkit/util/testutil/workers"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

func testClientGatewayCanceledCredentialsCallbackReturns(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureDirectPush)
	requiresLinux(t)

	ctx := sb.Context()

	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	username := "buildkit-user"
	password := "buildkit-pass"
	repo := "buildkit/auth-session-" + identity.NewID()
	backendRef := registry + "/" + repo + ":latest"

	st := llb.Scratch().File(llb.Mkfile("hello", 0o644, []byte("world")))
	def, err := st.Marshal(ctx)
	require.NoError(t, err)
	_, err = c.Solve(ctx, def, SolveOpt{
		Exports: []ExportEntry{{
			Type: ExporterImage,
			Attrs: map[string]string{
				"name": backendRef,
				"push": "true",
			},
		}},
	}, nil)
	require.NoError(t, err)

	target, err := url.Parse("http://" + registry)
	require.NoError(t, err)

	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Host = target.Host
		},
	}

	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, secret, ok := r.BasicAuth()
		if !ok || user != username || secret != password {
			w.Header().Set("WWW-Authenticate", `Basic realm="buildkit-test"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(proxyServer.Close)

	ref := strings.TrimPrefix(proxyServer.URL, "http://") + "/" + repo + ":latest"
	host, _, _ := strings.Cut(ref, "/")

	started := make(chan struct{})
	release := make(chan struct{})
	var startedOnce sync.Once

	provider := &blockingAuthProvider{
		t:           t,
		host:        host,
		username:    username,
		password:    password,
		started:     started,
		startedOnce: &startedOnce,
		release:     release,
	}

	_, err = c.Build(ctx, SolveOpt{
		Session: []session.Attachable{provider},
	}, "buildkit_test", func(ctx context.Context, gw client.Client) (*client.Result, error) {
		reqCtx, cancel := context.WithCancelCause(ctx)
		defer cancel(context.Canceled)

		errCh := make(chan error, 1)
		go func() {
			_, _, _, err := gw.ResolveImageConfig(reqCtx, ref, sourceresolver.Opt{})
			errCh <- err
		}()

		select {
		case <-started:
		case <-time.After(15 * time.Second):
			return nil, errors.New("timed out waiting for registry credential callback")
		}

		cancel(context.Canceled)

		select {
		case err := <-errCh:
			require.Error(t, err)
			return client.NewResult(), nil
		case <-time.After(3 * time.Second):
			close(release)
		}

		select {
		case err := <-errCh:
			require.Error(t, err)
		case <-time.After(15 * time.Second):
			return nil, errors.New("timed out draining canceled image config resolution")
		}

		return nil, errors.New("canceled image config resolution stayed blocked until the credentials callback was released")
	}, nil)
	require.NoError(t, err)

	checkAllReleasable(t, c, sb, true)
}

func testPullWithLayerLimit(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureDirectPush)
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	st := llb.Scratch().
		File(llb.Mkfile("/first", 0644, []byte("first"))).
		File(llb.Mkfile("/second", 0644, []byte("second"))).
		File(llb.Mkfile("/third", 0644, []byte("third")))

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	target := registry + "/buildkit/testlayers:latest"

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name": target,
					"push": "true",
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	// pull 2 first layers
	st = llb.Image(target, llb.WithLayerLimit(2)).
		File(llb.Mkfile("/forth", 0644, []byte("forth")))

	def, err = st.Marshal(sb.Context())
	require.NoError(t, err)

	destDir := t.TempDir()

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{{
			Type:      ExporterLocal,
			OutputDir: destDir,
		}},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "first"))
	require.NoError(t, err)
	require.Equal(t, "first", string(dt))

	dt, err = os.ReadFile(filepath.Join(destDir, "second"))
	require.NoError(t, err)
	require.Equal(t, "second", string(dt))

	_, err = os.ReadFile(filepath.Join(destDir, "third"))
	require.Error(t, err)
	require.True(t, errors.Is(err, os.ErrNotExist))

	dt, err = os.ReadFile(filepath.Join(destDir, "forth"))
	require.NoError(t, err)
	require.Equal(t, "forth", string(dt))

	// pull 3rd layer only
	st = llb.Diff(
		llb.Image(target, llb.WithLayerLimit(2)),
		llb.Image(target)).
		File(llb.Mkfile("/forth", 0644, []byte("forth")))

	def, err = st.Marshal(sb.Context())
	require.NoError(t, err)

	destDir = t.TempDir()

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{{
			Type:      ExporterLocal,
			OutputDir: destDir,
		}},
	}, nil)
	require.NoError(t, err)

	_, err = os.ReadFile(filepath.Join(destDir, "first"))
	require.Error(t, err)
	require.True(t, errors.Is(err, os.ErrNotExist))

	_, err = os.ReadFile(filepath.Join(destDir, "second"))
	require.Error(t, err)
	require.True(t, errors.Is(err, os.ErrNotExist))

	dt, err = os.ReadFile(filepath.Join(destDir, "third"))
	require.NoError(t, err)
	require.Equal(t, "third", string(dt))

	dt, err = os.ReadFile(filepath.Join(destDir, "forth"))
	require.NoError(t, err)
	require.Equal(t, "forth", string(dt))

	// zero limit errors cleanly
	st = llb.Image(target, llb.WithLayerLimit(0))

	def, err = st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid layer limit")
}

func testValidateDigestOrigin(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureDirectPush)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	imgName := integration.UnixOrWindows("busybox:latest", "nanoserver:latest")
	scratch := integration.UnixOrWindows(llb.Scratch(), llb.Image(imgName))

	cmdStr := integration.UnixOrWindows(
		"touch foo",
		"cmd /C echo a > foo",
	)
	st := llb.Image(imgName).
		Run(llb.Shlex(cmdStr), llb.Dir("/wd")).
		AddMount("/wd", scratch)

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	target := registry + "/buildkit/testdigest:latest"

	resp, err := c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name": target,
					"push": "true",
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	dgst, ok := resp.ExporterResponse[exptypes.ExporterImageDigestKey]
	require.True(t, ok)

	err = c.Prune(sb.Context(), nil, PruneAll)
	require.NoError(t, err)

	st = llb.Image(target + "@" + dgst)

	def, err = st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.NoError(t, err)

	// accessing the digest from invalid names should fail
	st = llb.Image("example.invalid/nosuchrepo@" + dgst)

	def, err = st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.Error(t, err)

	// also check repo that does exists but not digest
	st = llb.Image("docker.io/library/ubuntu@" + dgst)

	def, err = st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.Error(t, err)
}

type blockingAuthProvider struct {
	sessionauth.UnimplementedAuthServer

	t           *testing.T
	host        string
	username    string
	password    string
	started     chan struct{}
	startedOnce *sync.Once
	release     chan struct{}
}

func (p *blockingAuthProvider) Register(server *grpc.Server) {
	sessionauth.RegisterAuthServer(server, p)
}

func (p *blockingAuthProvider) Credentials(ctx context.Context, req *sessionauth.CredentialsRequest) (*sessionauth.CredentialsResponse, error) {
	require.Equal(p.t, p.host, req.Host)
	p.startedOnce.Do(func() {
		close(p.started)
	})

	select {
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	case <-p.release:
		return &sessionauth.CredentialsResponse{
			Username: p.username,
			Secret:   p.password,
		}, nil
	}
}
