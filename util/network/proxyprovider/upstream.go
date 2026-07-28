package proxyprovider

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"unicode/utf8"

	"golang.org/x/net/idna"
)

func upstreamProxyEnvironment() (bool, error) {
	var hasHTTPSProxy bool
	for _, names := range [][2]string{
		{"HTTP_PROXY", "http_proxy"},
		{"HTTPS_PROXY", "https_proxy"},
	} {
		name, value := proxyEnvironmentValue(names[0], names[1])
		if value == "" {
			continue
		}
		proxyURL, err := parseProxyEnvironmentValue(value)
		if err != nil {
			// Do not include value or err because either may contain proxy credentials.
			return false, fmt.Errorf("invalid %s", name)
		}
		if strings.EqualFold(proxyURL.Scheme, "https") {
			hasHTTPSProxy = true
		}
	}
	return hasHTTPSProxy, nil
}

func proxyEnvironmentValue(names ...string) (string, string) {
	for _, name := range names {
		if value := os.Getenv(name); value != "" {
			return name, value
		}
	}
	return "", ""
}

// parseProxyEnvironmentValue matches the URL handling used by
// http.ProxyFromEnvironment, including treating host[:port] as an HTTP proxy.
func parseProxyEnvironmentValue(value string) (*url.URL, error) {
	proxyURL, err := url.Parse(value)
	if err != nil || proxyURL.Scheme == "" || proxyURL.Host == "" {
		proxyURL, err = url.Parse("http://" + value)
	}
	if err != nil {
		return nil, err
	}
	if proxyURL.Hostname() == "" {
		return nil, fmt.Errorf("proxy URL with scheme %q is missing host", proxyURL.Scheme)
	}
	switch proxyURL.Scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return nil, fmt.Errorf("unsupported proxy URL scheme %q", proxyURL.Scheme)
	}
	return proxyURL, nil
}

func canonicalProxyAddr(proxyURL *url.URL) string {
	host := proxyURL.Hostname()
	if strings.IndexFunc(host, func(r rune) bool { return r >= utf8.RuneSelf }) >= 0 {
		if asciiHost, err := idna.Lookup.ToASCII(host); err == nil {
			host = asciiHost
		}
	}
	port := proxyURL.Port()
	if port == "" {
		if strings.EqualFold(proxyURL.Scheme, "https") {
			port = "443"
		} else {
			port = "80"
		}
	}
	return net.JoinHostPort(host, port)
}

type upstreamProxySelectionKey struct{}

type upstreamProxySelection struct {
	once      sync.Once
	proxyURL  *url.URL
	err       error
	httpsAddr string
}

func (s *upstreamProxySelection) proxyForRequest(req *http.Request, proxyFunc func(*http.Request) (*url.URL, error)) (*url.URL, error) {
	// Transport may retry a request while a dial from an earlier attempt is still
	// running. Pin the selection so surviving dials read only immutable
	// state and retries use the same policy decision.
	s.once.Do(func() {
		s.proxyURL, s.err = proxyFunc(req)
		if s.err == nil && s.proxyURL != nil && strings.EqualFold(s.proxyURL.Scheme, "https") {
			s.httpsAddr = canonicalProxyAddr(s.proxyURL)
		}
	})
	return s.proxyURL, s.err
}

type upstreamProxyRoundTripper struct {
	transport *http.Transport
}

func (t *upstreamProxyRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	selection := &upstreamProxySelection{}
	ctx := context.WithValue(req.Context(), upstreamProxySelectionKey{}, selection)
	return t.transport.RoundTrip(req.WithContext(ctx))
}

// configureTransportForUpstream disables HTTP/2 negotiation with an HTTPS
// proxy. Direct and tunneled origin connections retain HTTP/2 support. net/http
// sends CONNECT to forward proxies using HTTP/1.1 and does not support HTTP/2
// proxy connections.
func configureTransportForUpstream(transport *http.Transport) http.RoundTripper {
	proxyFunc := transport.Proxy
	if proxyFunc == nil {
		return transport
	}
	transport.Proxy = func(req *http.Request) (*url.URL, error) {
		selection, _ := req.Context().Value(upstreamProxySelectionKey{}).(*upstreamProxySelection)
		if selection == nil {
			return proxyFunc(req)
		}
		return selection.proxyForRequest(req, proxyFunc)
	}
	dialContext := transport.DialContext
	if dialContext == nil {
		dialer := &net.Dialer{}
		dialContext = dialer.DialContext
	}
	transport.DialTLSContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		conn, err := dialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		tlsConfig := transport.TLSClientConfig
		if tlsConfig == nil {
			tlsConfig = &tls.Config{}
		} else {
			tlsConfig = tlsConfig.Clone()
		}
		if tlsConfig.ServerName == "" {
			tlsConfig.ServerName, _, err = net.SplitHostPort(addr)
			if err != nil {
				_ = conn.Close()
				return nil, err
			}
		}
		selection, _ := ctx.Value(upstreamProxySelectionKey{}).(*upstreamProxySelection)
		if selection != nil && selection.httpsAddr == addr {
			tlsConfig.NextProtos = []string{"http/1.1"}
		}
		tlsConn := tls.Client(conn, tlsConfig)
		handshakeCtx := ctx
		if transport.TLSHandshakeTimeout != 0 {
			var cancel context.CancelFunc
			handshakeCtx, cancel = context.WithTimeout(ctx, transport.TLSHandshakeTimeout)
			defer cancel()
		}
		if err := tlsConn.HandshakeContext(handshakeCtx); err != nil {
			_ = conn.Close()
			return nil, err
		}
		return tlsConn, nil
	}
	return &upstreamProxyRoundTripper{transport: transport}
}
