//go:build linux

package proxyprovider

import (
	"compress/gzip"
	"container/list"
	"context"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/sourcepolicy"
	spb "github.com/moby/buildkit/sourcepolicy/pb"
	"github.com/moby/buildkit/util/network"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProxyHandlerCapturesGetMaterial(t *testing.T) {
	methodCh := make(chan string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methodCh <- r.Method
		_, _ = w.Write([]byte("proxy material"))
	}))
	t.Cleanup(upstream.Close)

	capture := network.NewProxyCapture()
	handler := newTestProxyHandler(t, capture)
	resp := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, upstream.URL+"/file", nil)

	handler.ServeHTTP(resp, req)

	require.Equal(t, http.StatusOK, resp.Code)
	require.Equal(t, "proxy material", resp.Body.String())
	require.Equal(t, http.MethodGet, <-methodCh)
	requests := capture.Requests()
	require.Len(t, requests, 1)
	require.Equal(t, http.MethodGet, requests[0].Method)
	require.Equal(t, upstream.URL+"/file", requests[0].URL)
	require.Equal(t, http.StatusOK, requests[0].StatusCode)
	materials := capture.Materials()
	require.Len(t, materials, 1)
	require.Equal(t, upstream.URL+"/file", materials[0].URL)
	require.Equal(t, "sha256:e352b3ec84adb842606c6d3638ac7466f5580f8617607ae6e0955f12130dd369", materials[0].Digest.String())
	require.Empty(t, capture.Incomplete())
}

func TestProxyHandlerDisablesUpstreamResponseTransforms(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Empty(t, r.Header.Values("Accept-Encoding"))
		w.Header().Set("Content-Encoding", "gzip")
		zw := gzip.NewWriter(w)
		_, _ = zw.Write([]byte("compressed proxy material"))
		assert.NoError(t, zw.Close())
	}))
	t.Cleanup(upstream.Close)

	capture := network.NewProxyCapture()
	handler := newTestProxyHandler(t, capture)
	resp := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, upstream.URL+"/file", nil)
	req.Header.Set("Accept-Encoding", "gzip")

	handler.ServeHTTP(resp, req)

	require.Equal(t, http.StatusOK, resp.Code)
	require.Equal(t, "gzip", resp.Header().Get("Content-Encoding"))
	require.Empty(t, capture.Incomplete())
	require.Len(t, capture.Materials(), 1)
}

func TestProxyHandlerRoundTripIgnoresClientContextCancel(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(upstream.Close)

	pool := x509.NewCertPool()
	pool.AddCert(upstream.Certificate())
	handler := newTestProxyHandler(t, nil)
	handler.transport.TLSClientConfig = upstream.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	handler.transport.TLSClientConfig.RootCAs = pool

	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(context.Canceled)
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, upstream.URL, nil)

	resp, err := handler.roundTrip(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestProxyHandlerMarksPostIncomplete(t *testing.T) {
	methodCh := make(chan string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methodCh <- r.Method
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(upstream.Close)

	capture := network.NewProxyCapture()
	handler := newTestProxyHandler(t, capture)
	resp := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, upstream.URL+"/token", nil)

	handler.ServeHTTP(resp, req)

	require.Equal(t, http.StatusOK, resp.Code)
	require.Equal(t, http.MethodPost, <-methodCh)
	require.Empty(t, capture.Materials())
	incomplete := capture.Incomplete()
	require.Len(t, incomplete, 1)
	require.Equal(t, http.MethodPost, incomplete[0].Method)
	require.Equal(t, upstream.URL+"/token", incomplete[0].URL)
	require.Equal(t, "method_not_materializable", incomplete[0].Reason)
}

func TestProxyHandlerCapturesRedirectMaterialAlias(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redirect":
			http.Redirect(w, r, "/next", http.StatusFound)
		case "/next":
			_, _ = w.Write([]byte("redirect material"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	capture := network.NewProxyCapture()
	handler := newTestProxyHandler(t, capture)
	resp := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, upstream.URL+"/redirect", nil)

	handler.ServeHTTP(resp, req)

	require.Equal(t, http.StatusFound, resp.Code)
	require.Empty(t, capture.Materials())
	resp = httptest.NewRecorder()
	req = httptest.NewRequestWithContext(t.Context(), http.MethodGet, upstream.URL+"/next", nil)

	handler.ServeHTTP(resp, req)

	require.Equal(t, http.StatusOK, resp.Code)
	expectedDigest := "sha256:230b890186495c4878036c4393de6137ca1a3d0e51899ea6402eaef3320a9e9b"
	requests := capture.Requests()
	require.Len(t, requests, 2)
	require.Equal(t, upstream.URL+"/redirect", requests[0].URL)
	require.Equal(t, upstream.URL+"/next", requests[0].RedirectTarget)
	require.Equal(t, http.StatusFound, requests[0].StatusCode)
	require.Equal(t, upstream.URL+"/next", requests[1].URL)
	require.Empty(t, requests[1].RedirectTarget)
	require.Equal(t, http.StatusOK, requests[1].StatusCode)
	materials := capture.Materials()
	require.Len(t, materials, 2)
	require.Equal(t, upstream.URL+"/next", materials[0].URL)
	require.Equal(t, expectedDigest, materials[0].Digest.String())
	require.Equal(t, upstream.URL+"/redirect", materials[1].URL)
	require.Equal(t, expectedDigest, materials[1].Digest.String())
	require.Empty(t, capture.Incomplete())
}

func TestProxyHandlerRedactsCapturedCredentials(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("secret ok"))
	}))
	t.Cleanup(upstream.Close)

	capture := network.NewProxyCapture()
	handler := newTestProxyHandler(t, capture)
	resp := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, strings.Replace(upstream.URL, "http://", "http://user:pass@", 1)+"/file", nil)

	handler.ServeHTTP(resp, req)

	require.Equal(t, http.StatusOK, resp.Code)
	materials := capture.Materials()
	require.Len(t, materials, 1)
	require.NotContains(t, materials[0].URL, "user")
	require.NotContains(t, materials[0].URL, "pass")
	require.Contains(t, materials[0].URL, "xxxxx:xxxxx@")
	requests := capture.Requests()
	require.Len(t, requests, 1)
	require.NotContains(t, requests[0].URL, "user")
	require.NotContains(t, requests[0].URL, "pass")
	require.Contains(t, requests[0].URL, "xxxxx:xxxxx@")
	require.Equal(t, http.StatusOK, requests[0].StatusCode)
}

func TestCaptureURLNormalizesDefaultPort(t *testing.T) {
	require.Equal(t,
		"https://dl-cdn.alpinelinux.org/alpine/v3.23/main/aarch64/APKINDEX.tar.gz",
		captureURL("https://dl-cdn.alpinelinux.org:443/alpine/v3.23/main/aarch64/APKINDEX.tar.gz"),
	)
	require.Equal(t,
		"http://example.com/file",
		captureURL("http://example.com:80/file"),
	)
	require.Equal(t,
		"https://example.com:8443/file",
		captureURL("https://example.com:8443/file"),
	)
	require.Equal(t,
		"https://xxxxx:xxxxx@example.com/file",
		captureURL("https://user:pass@example.com:443/file"),
	)
	require.Equal(t,
		"https://[2001:db8::1]/file",
		captureURL("https://[2001:db8::1]:443/file"),
	)
	require.Equal(t,
		"https://[2001:db8::1]:8443/file",
		captureURL("https://[2001:db8::1]:8443/file"),
	)
}

func TestProxyHandlerAppliesPolicyConvert(t *testing.T) {
	original := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("original upstream should not receive converted request")
	}))
	t.Cleanup(original.Close)
	mirrorMethodCh := make(chan string, 1)
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mirrorMethodCh <- r.Method
		_, _ = w.Write([]byte("mirror material"))
	}))
	t.Cleanup(mirror.Close)

	capture := network.NewProxyCapture()
	handler := newTestProxyHandler(t, capture)
	handler.policy = proxyPolicyFunc(func(_ context.Context, op *pb.Op) (bool, error) {
		require.Equal(t, original.URL+"/file", op.GetSource().Identifier)
		op.GetSource().Identifier = mirror.URL + "/file"
		return true, nil
	})
	resp := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, original.URL+"/file", nil)

	handler.ServeHTTP(resp, req)

	require.Equal(t, http.StatusOK, resp.Code)
	require.Equal(t, "mirror material", resp.Body.String())
	require.Equal(t, http.MethodGet, <-mirrorMethodCh)
	requests := capture.Requests()
	require.Len(t, requests, 1)
	require.Equal(t, mirror.URL+"/file", requests[0].URL)
	require.Equal(t, http.StatusOK, requests[0].StatusCode)
	materials := capture.Materials()
	require.Len(t, materials, 1)
	require.Equal(t, mirror.URL+"/file", materials[0].URL)
	require.Empty(t, capture.Incomplete())
}

func TestProxyHandlerPolicyRedactsCredentialsInErrors(t *testing.T) {
	handler := newTestProxyHandler(t, nil)
	handler.policy = enginePolicyEvaluator{engine: sourcepolicy.NewEngine([]*spb.Policy{
		{
			Rules: []*spb.Rule{
				{
					Action: spb.PolicyAction_DENY,
					Selector: &spb.Selector{
						Identifier: "https://*",
					},
				},
			},
		},
	})}

	_, err := handler.check(t.Context(), http.MethodGet, "https://user:pass@example.com/path")
	require.ErrorIs(t, err, sourcepolicy.ErrSourceDenied)
	require.NotContains(t, err.Error(), "user")
	require.NotContains(t, err.Error(), "pass")
	require.Contains(t, err.Error(), "https://xxxxx:xxxxx@example.com/path")
}

func TestProxyHandlerRejectsConvertedNonGetRequest(t *testing.T) {
	handler := newTestProxyHandler(t, nil)
	handler.policy = proxyPolicyFunc(func(_ context.Context, op *pb.Op) (bool, error) {
		op.GetSource().Identifier = "https://mirror.example.com/file"
		return true, nil
	})

	_, err := handler.check(t.Context(), http.MethodPost, "https://example.com/file")
	require.Error(t, err)
	require.Contains(t, err.Error(), "conversion is only supported for GET")
}

func TestProxyHandlerRejectsConvertedAttrs(t *testing.T) {
	handler := newTestProxyHandler(t, nil)
	handler.policy = proxyPolicyFunc(func(_ context.Context, op *pb.Op) (bool, error) {
		op.GetSource().Identifier = "https://mirror.example.com/file"
		op.GetSource().Attrs = map[string]string{
			pb.AttrHTTPChecksum: "sha256:6e4b94fc270e708e1068be28bd3551dc6917a4fc5a61293d51bb36e6b75c4b53",
		}
		return true, nil
	})

	_, err := handler.check(t.Context(), http.MethodGet, "https://example.com/file")
	require.Error(t, err)
	require.Contains(t, err.Error(), "proxy conversion only supports URL updates")
}

func TestCertForHostUsesCachedValidCertificate(t *testing.T) {
	p := newTestCertProvider(t)

	cert, err := p.certForHost("example.com")
	require.NoError(t, err)
	cached, err := p.certForHost("example.com")
	require.NoError(t, err)

	require.Same(t, cert, cached)
	require.Len(t, p.certs, 1)
}

func TestCertForHostRefreshesExpiredCertificate(t *testing.T) {
	p := newTestCertProvider(t)

	cert, err := p.certForHost("example.com")
	require.NoError(t, err)
	p.certsMu.Lock()
	p.certs["example.com"].expires = time.Now().Add(-time.Second)
	p.certsMu.Unlock()

	refreshed, err := p.certForHost("example.com")
	require.NoError(t, err)

	require.NotSame(t, cert, refreshed)
	require.NotEqual(t, cert.Certificate[0], refreshed.Certificate[0])
	require.Len(t, p.certs, 1)
}

type proxyPolicyFunc func(context.Context, *pb.Op) (bool, error)

func (f proxyPolicyFunc) Evaluate(ctx context.Context, op *pb.Op) (bool, error) {
	return f(ctx, op)
}

type enginePolicyEvaluator struct {
	engine *sourcepolicy.Engine
}

func (e enginePolicyEvaluator) Evaluate(ctx context.Context, op *pb.Op) (bool, error) {
	return e.engine.Evaluate(ctx, op.GetSource())
}

func newTestProxyHandler(t *testing.T, capture *network.ProxyCapture) *proxyHandler {
	t.Helper()
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DisableCompression = true
	t.Cleanup(tr.CloseIdleConnections)
	return &proxyHandler{
		provider:  &provider{},
		capture:   capture,
		transport: tr,
	}
}

func newTestCertProvider(t *testing.T) *provider {
	t.Helper()
	certPEM, ca, key, err := newCA()
	require.NoError(t, err)
	require.NotEmpty(t, certPEM)
	return &provider{
		caPEM: certPEM,
		ca:    ca,
		caKey: key,
		certs: map[string]*certCacheEntry{},
		lru:   list.New(),
	}
}
