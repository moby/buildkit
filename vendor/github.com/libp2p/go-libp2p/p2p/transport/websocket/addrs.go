package websocket

import (
	"fmt"
	"net"
	"net/url"
	"strconv"

	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

// Addr is an implementation of net.Addr for WebSocket.
type Addr struct {
	*url.URL
}

var _ net.Addr = (*Addr)(nil)

// Network returns the network type for a WebSocket, "websocket".
func (addr *Addr) Network() string {
	return "websocket"
}

// NewAddr creates an Addr with `ws` scheme (insecure).
//
// Deprecated. Use NewAddrWithScheme.
func NewAddr(host string) *Addr {
	// Older versions of the transport only supported insecure connections (i.e.
	// WS instead of WSS). Assume that is the case here.
	return NewAddrWithScheme(host, false)
}

// NewAddrWithScheme creates a new Addr using the given host string. isSecure
// should be true for WSS connections and false for WS.
func NewAddrWithScheme(host string, isSecure bool) *Addr {
	scheme := "ws"
	if isSecure {
		scheme = "wss"
	}
	return &Addr{
		URL: &url.URL{
			Scheme: scheme,
			Host:   host,
		},
	}
}

func ConvertWebsocketMultiaddrToNetAddr(maddr ma.Multiaddr) (net.Addr, error) {
	url, err := parseMultiaddr(maddr)
	if err != nil {
		return nil, err
	}
	return &Addr{URL: url}, nil
}

func ParseWebsocketNetAddr(a net.Addr) (ma.Multiaddr, error) {
	wsa, ok := a.(*Addr)
	if !ok {
		return nil, fmt.Errorf("not a websocket address")
	}

	var (
		tcpma ma.Multiaddr
		err   error
		port  int
		host  = wsa.Hostname()
	)

	// Get the port
	if portStr := wsa.Port(); portStr != "" {
		port, err = strconv.Atoi(portStr)
		if err != nil {
			return nil, fmt.Errorf("failed to parse port '%q': %s", portStr, err)
		}
	} else {
		return nil, fmt.Errorf("invalid port in url: '%q'", wsa.URL)
	}

	// NOTE: Ignoring IPv6 zones...
	// Detect if host is IP address or DNS
	if ip := net.ParseIP(host); ip != nil {
		// Assume IP address
		tcpma, err = manet.FromNetAddr(&net.TCPAddr{
			IP:   ip,
			Port: port,
		})
		if err != nil {
			return nil, err
		}
	} else {
		// Assume DNS name
		tcpma, err = ma.NewMultiaddr(fmt.Sprintf("/dns/%s/tcp/%d", host, port))
		if err != nil {
			return nil, err
		}
	}

	wsma, err := ma.NewMultiaddr("/" + wsa.Scheme)
	if err != nil {
		return nil, err
	}

	return tcpma.Encapsulate(wsma), nil
}

func parseMultiaddr(maddr ma.Multiaddr) (*url.URL, error) {
	// Only look at the _last_ component.
	maddr, wscomponent := ma.SplitLast(maddr)
	if maddr == nil || wscomponent == nil {
		return nil, fmt.Errorf("websocket addrs need at least two components")
	}

	var scheme string
	switch wscomponent.Protocol().Code {
	case ma.P_WS:
		scheme = "ws"
	case ma.P_WSS:
		scheme = "wss"
	default:
		return nil, fmt.Errorf("not a websocket multiaddr")
	}

	network, host, err := manet.DialArgs(maddr)
	if err != nil {
		return nil, err
	}
	switch network {
	case "tcp", "tcp4", "tcp6":
	default:
		return nil, fmt.Errorf("unsupported websocket network %s", network)
	}
	return &url.URL{
		Scheme: scheme,
		Host:   host,
	}, nil
}
