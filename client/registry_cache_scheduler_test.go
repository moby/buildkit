package client

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/moby/buildkit/util/testutil/workers"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
)

// A registry cache import that has response headers but no body must not hold
// the scheduler. Another build on the same daemon has to finish first.
func testRegistryCacheImportSchedulerIsolation(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureCacheExport, workers.FeatureCacheImport, workers.FeatureCacheBackendRegistry)
	ctx, cancel := context.WithTimeout(sb.Context(), 2*time.Minute)
	defer cancel()
	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()
	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	repo := "/buildkit/scheduler-cache-" + identity.NewID()
	// Cache export skips root-only records, so include a dependent file operation.
	def, err := llb.Scratch().File(llb.Mkfile("/payload", 0o644, []byte("cached"))).File(llb.Mkfile("/marker", 0o644, nil)).Marshal(ctx)
	require.NoError(t, err)
	_, err = c.Solve(ctx, def, SolveOpt{
		Exports:      []ExportEntry{{Type: ExporterLocal, OutputDir: t.TempDir()}},
		CacheExports: []CacheOptionsEntry{{Type: "registry", Attrs: map[string]string{"ref": registry + repo + ":latest", "mode": "max"}}},
	}, nil)
	require.NoError(t, err)
	ensurePruneAll(t, c, sb)

	started, release := make(chan struct{}), make(chan struct{})
	var startedOnce, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target := *r.URL
		target.Scheme, target.Host = "http", registry
		req := r.Clone(r.Context())
		req.URL, req.Host, req.RequestURI = &target, registry, ""
		resp, err := http.DefaultTransport.RoundTrip(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for k, values := range resp.Header {
			w.Header()[k] = values
		}
		w.WriteHeader(resp.StatusCode)
		if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/manifests/") && resp.StatusCode == http.StatusOK {
			w.(http.Flusher).Flush()
			startedOnce.Do(func() { close(started) })
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		_, _ = io.Copy(w, resp.Body)
	}))
	defer proxy.Close()
	defer unblock()

	proxyURL, err := url.Parse(proxy.URL)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() {
		_, err := c.Solve(ctx, def, SolveOpt{
			Exports:      []ExportEntry{{Type: ExporterLocal, OutputDir: t.TempDir()}},
			CacheImports: []CacheOptionsEntry{{Type: "registry", Attrs: map[string]string{"ref": proxyURL.Host + repo + ":latest", "registry.insecure": "true"}}},
		}, nil)
		done <- err
	}()
	select {
	case <-started:
	case err := <-done:
		t.Fatalf("import ended before stalled response: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	independentCtx, cancelIndependent := context.WithTimeout(ctx, 15*time.Second)
	defer cancelIndependent()
	independent, err := llb.Scratch().File(llb.Mkfile("/out-"+identity.NewID(), 0o644, []byte("independent"))).Marshal(independentCtx)
	require.NoError(t, err)
	_, err = c.Solve(independentCtx, independent, SolveOpt{}, nil)
	require.NoError(t, err)

	unblock()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
