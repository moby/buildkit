//go:build linux

package proxyprovider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/sourcepolicy"
	spb "github.com/moby/buildkit/sourcepolicy/pb"
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
	req := httptest.NewRequest(http.MethodGet, original.URL+"/file", nil)

	handler.ServeHTTP(resp, req)

	require.Equal(t, http.StatusOK, resp.Code)
	require.Equal(t, "mirror material", resp.Body.String())
	require.Equal(t, http.MethodGet, <-mirrorMethodCh)
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
	t.Cleanup(tr.CloseIdleConnections)
	return &proxyHandler{
		provider: &provider{client: tr},
		capture:  capture,
	}
}
