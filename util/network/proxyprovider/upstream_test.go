package proxyprovider

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUpstreamProxyEnvironment(t *testing.T) {
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
		t.Setenv(name, "")
	}
	hasHTTPSProxy, err := upstreamProxyEnvironment()
	require.NoError(t, err)
	require.False(t, hasHTTPSProxy)

	t.Setenv("HTTPS_PROXY", "http://proxy.example:3128")
	hasHTTPSProxy, err = upstreamProxyEnvironment()
	require.NoError(t, err)
	require.False(t, hasHTTPSProxy)

	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("https_proxy", "https://proxy.example:443")
	hasHTTPSProxy, err = upstreamProxyEnvironment()
	require.NoError(t, err)
	require.True(t, hasHTTPSProxy)

	// The uppercase value takes precedence, matching http.ProxyFromEnvironment.
	t.Setenv("HTTPS_PROXY", "http://proxy.example:3128")
	hasHTTPSProxy, err = upstreamProxyEnvironment()
	require.NoError(t, err)
	require.False(t, hasHTTPSProxy)
}

func TestParseProxyEnvironmentValue(t *testing.T) {
	for _, tc := range []struct {
		value      string
		wantScheme string
		wantHost   string
	}{
		{value: "proxy.example:3128", wantScheme: "http", wantHost: "proxy.example:3128"},
		{value: "http://proxy.example:3128", wantScheme: "http", wantHost: "proxy.example:3128"},
		{value: "https://proxy.example", wantScheme: "https", wantHost: "proxy.example"},
		{value: "socks5://proxy.example:1080", wantScheme: "socks5", wantHost: "proxy.example:1080"},
		{value: "socks5h://proxy.example:1080", wantScheme: "socks5h", wantHost: "proxy.example:1080"},
	} {
		t.Run(tc.value, func(t *testing.T) {
			proxyURL, err := parseProxyEnvironmentValue(tc.value)
			require.NoError(t, err)
			require.Equal(t, tc.wantScheme, proxyURL.Scheme)
			require.Equal(t, tc.wantHost, proxyURL.Host)
		})
	}
}

func TestUpstreamProxyEnvironmentRejectsInvalidURL(t *testing.T) {
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
		t.Setenv(name, "")
	}

	t.Setenv("HTTP_PROXY", "http://user:secret@proxy.example/%zz")
	_, err := upstreamProxyEnvironment()
	require.EqualError(t, err, "invalid HTTP_PROXY")
	require.NotContains(t, err.Error(), "secret")

	t.Setenv("HTTP_PROXY", "")
	t.Setenv("https_proxy", "http://user:other-secret@proxy.example/%zz")
	_, err = upstreamProxyEnvironment()
	require.EqualError(t, err, "invalid https_proxy")
	require.NotContains(t, err.Error(), "other-secret")

	t.Setenv("https_proxy", "")
	t.Setenv("HTTP_PROXY", "/")
	_, err = upstreamProxyEnvironment()
	require.EqualError(t, err, "invalid HTTP_PROXY")

	t.Setenv("HTTP_PROXY", "http://:80")
	_, err = upstreamProxyEnvironment()
	require.EqualError(t, err, "invalid HTTP_PROXY")

	t.Setenv("HTTP_PROXY", "ftp://user:scheme-secret@proxy.example:21")
	_, err = upstreamProxyEnvironment()
	require.EqualError(t, err, "invalid HTTP_PROXY")
	require.NotContains(t, err.Error(), "scheme-secret")
}

func TestHTTPSUpstreamProxySelectionIsStableAcrossRetries(t *testing.T) {
	proxyURL, err := url.Parse("https://proxy.example:443")
	require.NoError(t, err)

	var calls int
	transport := &http.Transport{
		Proxy: func(*http.Request) (*url.URL, error) {
			calls++
			if calls > 1 {
				return nil, errors.New("proxy selection changed on retry")
			}
			return proxyURL, nil
		},
	}
	roundTripper := configureTransportForUpstream(transport)
	_, ok := roundTripper.(*upstreamProxyRoundTripper)
	require.True(t, ok)

	selection := &upstreamProxySelection{}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://destination.example", nil)
	require.NoError(t, err)
	req = req.WithContext(context.WithValue(req.Context(), upstreamProxySelectionKey{}, selection))

	first, err := transport.Proxy(req)
	require.NoError(t, err)
	second, err := transport.Proxy(req)
	require.NoError(t, err)
	require.Equal(t, proxyURL, first)
	require.Equal(t, proxyURL, second)
	require.Equal(t, 1, calls)
	require.Equal(t, "proxy.example:443", selection.httpsAddr)
}

func TestHTTPSUpstreamProxyUsesHTTP1(t *testing.T) {
	originProtocol := make(chan string, 1)
	originServer := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originProtocol <- r.Proto
		w.WriteHeader(http.StatusNoContent)
	}))
	originServer.EnableHTTP2 = true
	originServer.StartTLS()
	t.Cleanup(originServer.Close)

	type proxyRequest struct {
		method   string
		protocol string
	}
	proxyRequests := make(chan proxyRequest, 1)
	proxyServer := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyRequests <- proxyRequest{method: r.Method, protocol: r.Proto}
		upstreamConn, err := net.Dial("tcp", originServer.Listener.Addr().String())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		downstreamConn, rw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			_ = upstreamConn.Close()
			return
		}
		_, _ = rw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		_ = rw.Flush()
		go func() {
			_, _ = io.Copy(upstreamConn, downstreamConn)
			_ = upstreamConn.Close()
		}()
		_, _ = io.Copy(downstreamConn, upstreamConn)
		_ = downstreamConn.Close()
	}))
	proxyServer.EnableHTTP2 = true
	proxyServer.StartTLS()
	t.Cleanup(proxyServer.Close)

	_, proxyPort, err := net.SplitHostPort(proxyServer.Listener.Addr().String())
	require.NoError(t, err)
	proxyURL, err := url.Parse("https://" + net.JoinHostPort("bücher.example", proxyPort))
	require.NoError(t, err)

	serverTransport := proxyServer.Client().Transport.(*http.Transport)
	tlsConfig := serverTransport.TLSClientConfig.Clone()
	tlsConfig.ServerName = "example.com"
	dialAddr := make(chan string, 1)
	transport := &http.Transport{
		TLSClientConfig:   tlsConfig,
		ForceAttemptHTTP2: true,
		Proxy:             http.ProxyURL(proxyURL),
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			dialAddr <- addr
			return (&net.Dialer{}).DialContext(ctx, network, proxyServer.Listener.Addr().String())
		},
	}
	roundTripper := configureTransportForUpstream(transport)
	t.Cleanup(transport.CloseIdleConnections)

	client := &http.Client{Transport: roundTripper}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://destination.example.com", nil)
	require.NoError(t, err)
	resp, err := client.Do(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	require.Equal(t, proxyRequest{method: http.MethodConnect, protocol: "HTTP/1.1"}, <-proxyRequests)
	require.Equal(t, "HTTP/2.0", <-originProtocol)
	require.Equal(t, canonicalProxyAddr(proxyURL), <-dialAddr)
}

func TestHTTPSUpstreamProxyPreservesHTTP2ForDirect(t *testing.T) {
	protocol := make(chan string, 1)
	origin := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		protocol <- r.Proto
		w.WriteHeader(http.StatusNoContent)
	}))
	origin.EnableHTTP2 = true
	origin.StartTLS()
	t.Cleanup(origin.Close)

	transport := origin.Client().Transport.(*http.Transport).Clone()
	transport.ForceAttemptHTTP2 = true
	transport.Proxy = func(*http.Request) (*url.URL, error) { return nil, nil }
	roundTripper := configureTransportForUpstream(transport)
	t.Cleanup(transport.CloseIdleConnections)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, origin.URL, nil)
	require.NoError(t, err)

	resp, err := roundTripper.RoundTrip(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	require.Equal(t, "HTTP/2.0", <-protocol)
}
