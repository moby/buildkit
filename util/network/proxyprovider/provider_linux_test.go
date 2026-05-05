//go:build linux

package proxyprovider

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/moby/buildkit/util/network"
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
	req := httptest.NewRequest(http.MethodGet, upstream.URL+"/file", nil)

	handler.ServeHTTP(resp, req)

	require.Equal(t, http.StatusOK, resp.Code)
	require.Equal(t, "proxy material", resp.Body.String())
	require.Equal(t, http.MethodGet, <-methodCh)
	materials := capture.Materials()
	require.Len(t, materials, 1)
	require.Equal(t, upstream.URL+"/file", materials[0].URL)
	require.Equal(t, "sha256:e352b3ec84adb842606c6d3638ac7466f5580f8617607ae6e0955f12130dd369", materials[0].Digest.String())
	require.Empty(t, capture.Incomplete())
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
	req := httptest.NewRequest(http.MethodPost, upstream.URL+"/token", nil)

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

func TestProxyHandlerSkipsRedirectMaterial(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/next", http.StatusFound)
	}))
	t.Cleanup(upstream.Close)

	capture := network.NewProxyCapture()
	handler := newTestProxyHandler(t, capture)
	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, upstream.URL+"/redirect", nil)

	handler.ServeHTTP(resp, req)

	require.Equal(t, http.StatusFound, resp.Code)
	require.Empty(t, capture.Materials())
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
	req := httptest.NewRequest(http.MethodGet, strings.Replace(upstream.URL, "http://", "http://user:pass@", 1)+"/file", nil)

	handler.ServeHTTP(resp, req)

	require.Equal(t, http.StatusOK, resp.Code)
	materials := capture.Materials()
	require.Len(t, materials, 1)
	require.NotContains(t, materials[0].URL, "user")
	require.NotContains(t, materials[0].URL, "pass")
	require.Contains(t, materials[0].URL, "xxxxx:xxxxx@")
}

func newTestProxyHandler(t *testing.T, capture *network.ProxyCapture) *proxyHandler {
	t.Helper()
	tr := http.DefaultTransport.(*http.Transport).Clone()
	t.Cleanup(tr.CloseIdleConnections)
	return &proxyHandler{
		provider: &provider{client: tr},
		capture:  capture,
	}
}
