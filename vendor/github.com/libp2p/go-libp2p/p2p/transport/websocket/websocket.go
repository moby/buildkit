// Package websocket implements a websocket based transport for go-libp2p.
package websocket

import (
	"context"
	"crypto/tls"
	"net/http"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/transport"

	ma "github.com/multiformats/go-multiaddr"
	mafmt "github.com/multiformats/go-multiaddr-fmt"
	manet "github.com/multiformats/go-multiaddr/net"

	ws "github.com/gorilla/websocket"
)

// WsFmt is multiaddr formatter for WsProtocol
var WsFmt = mafmt.And(mafmt.TCP, mafmt.Base(ma.P_WS))

// This is _not_ WsFmt because we want the transport to stick to dialing fully
// resolved addresses.
var dialMatcher = mafmt.And(mafmt.Or(mafmt.IP, mafmt.DNS), mafmt.Base(ma.P_TCP), mafmt.Or(mafmt.Base(ma.P_WS), mafmt.Base(ma.P_WSS)))

func init() {
	manet.RegisterFromNetAddr(ParseWebsocketNetAddr, "websocket")
	manet.RegisterToNetAddr(ConvertWebsocketMultiaddrToNetAddr, "ws")
	manet.RegisterToNetAddr(ConvertWebsocketMultiaddrToNetAddr, "wss")
}

// Default gorilla upgrader
var upgrader = ws.Upgrader{
	// Allow requests from *all* origins.
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

type Option func(*WebsocketTransport) error

// WithTLSClientConfig sets a TLS client configuration on the WebSocket Dialer. Only
// relevant for non-browser usages.
//
// Some useful use cases include setting InsecureSkipVerify to `true`, or
// setting user-defined trusted CA certificates.
func WithTLSClientConfig(c *tls.Config) Option {
	return func(t *WebsocketTransport) error {
		t.tlsClientConf = c
		return nil
	}
}

// WithTLSConfig sets a TLS configuration for the WebSocket listener.
func WithTLSConfig(conf *tls.Config) Option {
	return func(t *WebsocketTransport) error {
		t.tlsConf = conf
		return nil
	}
}

// WebsocketTransport is the actual go-libp2p transport
type WebsocketTransport struct {
	upgrader transport.Upgrader
	rcmgr    network.ResourceManager

	tlsClientConf *tls.Config
	tlsConf       *tls.Config
}

var _ transport.Transport = (*WebsocketTransport)(nil)

func New(u transport.Upgrader, rcmgr network.ResourceManager, opts ...Option) (*WebsocketTransport, error) {
	if rcmgr == nil {
		rcmgr = network.NullResourceManager
	}
	t := &WebsocketTransport{
		upgrader: u,
		rcmgr:    rcmgr,
	}
	for _, opt := range opts {
		if err := opt(t); err != nil {
			return nil, err
		}
	}
	return t, nil
}

func (t *WebsocketTransport) CanDial(a ma.Multiaddr) bool {
	return dialMatcher.Matches(a)
}

func (t *WebsocketTransport) Protocols() []int {
	return []int{ma.P_WS, ma.P_WSS}
}

func (t *WebsocketTransport) Proxy() bool {
	return false
}

func (t *WebsocketTransport) Dial(ctx context.Context, raddr ma.Multiaddr, p peer.ID) (transport.CapableConn, error) {
	connScope, err := t.rcmgr.OpenConnection(network.DirOutbound, true, raddr)
	if err != nil {
		return nil, err
	}
	macon, err := t.maDial(ctx, raddr)
	if err != nil {
		connScope.Done()
		return nil, err
	}
	return t.upgrader.Upgrade(ctx, t, macon, network.DirOutbound, p, connScope)
}

func (t *WebsocketTransport) maDial(ctx context.Context, raddr ma.Multiaddr) (manet.Conn, error) {
	wsurl, err := parseMultiaddr(raddr)
	if err != nil {
		return nil, err
	}
	isWss := wsurl.Scheme == "wss"
	dialer := ws.Dialer{HandshakeTimeout: 30 * time.Second}
	if isWss {
		dialer.TLSClientConfig = t.tlsClientConf

	}
	wscon, _, err := dialer.DialContext(ctx, wsurl.String(), nil)
	if err != nil {
		return nil, err
	}

	mnc, err := manet.WrapNetConn(NewConn(wscon, isWss))
	if err != nil {
		wscon.Close()
		return nil, err
	}
	return mnc, nil
}

func (t *WebsocketTransport) maListen(a ma.Multiaddr) (manet.Listener, error) {
	l, err := newListener(a, t.tlsConf)
	if err != nil {
		return nil, err
	}
	go l.serve()
	return l, nil
}

func (t *WebsocketTransport) Listen(a ma.Multiaddr) (transport.Listener, error) {
	malist, err := t.maListen(a)
	if err != nil {
		return nil, err
	}
	return t.upgrader.UpgradeListener(t, malist), nil
}
