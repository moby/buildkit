package client

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"time"

	libp2p "github.com/libp2p/go-libp2p"
	p2phttp "github.com/libp2p/go-libp2p-http"
	peer "github.com/libp2p/go-libp2p/core/peer"
	peerstore "github.com/libp2p/go-libp2p/core/peerstore"
	noise "github.com/libp2p/go-libp2p/p2p/security/noise"
	libp2ptls "github.com/libp2p/go-libp2p/p2p/security/tls"
	tcp "github.com/libp2p/go-libp2p/p2p/transport/tcp"
	websocket "github.com/libp2p/go-libp2p/p2p/transport/websocket"
	madns "github.com/multiformats/go-multiaddr-dns"
	manet "github.com/multiformats/go-multiaddr/net"
	"github.com/tv42/httpunix"
)

// This is essentially a http.DefaultTransport. We should not mess
// with it since it's a global variable, and we don't know who else uses
// it, so we create our own.
// TODO: Allow more configuration options.
func (c *defaultClient) defaultTransport() {
	c.transport = &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
			DualStack: true,
		}).DialContext,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	c.net = "http"
}

func (c *defaultClient) enableLibp2p() error {
	c.defaultTransport()

	pinfo, err := peer.AddrInfoFromP2pAddr(c.config.APIAddr)
	if err != nil {
		return err
	}

	if len(pinfo.Addrs) == 0 {
		return errors.New("APIAddr only includes a Peer ID")
	}

	if c.config.ProtectorKey != nil && len(c.config.ProtectorKey) > 0 {
		if len(c.config.ProtectorKey) != 32 {
			return errors.New("length of ProtectorKey should be 32")
		}
	}

	transports := libp2p.DefaultTransports
	if c.config.ProtectorKey != nil {
		transports = libp2p.ChainOptions(
			libp2p.NoTransports,
			libp2p.Transport(tcp.NewTCPTransport),
			libp2p.Transport(websocket.New),
		)
	}

	h, err := libp2p.New(
		libp2p.PrivateNetwork(c.config.ProtectorKey),
		libp2p.Security(noise.ID, noise.New),
		libp2p.Security(libp2ptls.ID, libp2ptls.New),
		transports,
	)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(c.ctx, ResolveTimeout)
	defer cancel()
	resolvedAddrs, err := madns.Resolve(ctx, pinfo.Addrs[0])
	if err != nil {
		return err
	}

	h.Peerstore().AddAddrs(pinfo.ID, resolvedAddrs, peerstore.PermanentAddrTTL)
	c.transport.RegisterProtocol("libp2p", p2phttp.NewTransport(h))
	c.net = "libp2p"
	c.p2p = h
	c.hostname = pinfo.ID.String()
	return nil
}

func (c *defaultClient) enableTLS() error {
	c.defaultTransport()
	// based on https://github.com/denji/golang-tls
	c.transport.TLSClientConfig = &tls.Config{
		MinVersion:               tls.VersionTLS12,
		CurvePreferences:         []tls.CurveID{tls.CurveP521, tls.CurveP384, tls.CurveP256},
		PreferServerCipherSuites: true,
		CipherSuites: []uint16{
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA,
			tls.TLS_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_RSA_WITH_AES_256_CBC_SHA,
		},
		InsecureSkipVerify: c.config.NoVerifyCert,
	}
	c.net = "https"
	return nil
}

func (c *defaultClient) enableUnix() error {
	c.defaultTransport()
	unixTransport := &httpunix.Transport{
		DialTimeout: time.Second,
	}
	_, addr, err := manet.DialArgs(c.config.APIAddr)
	if err != nil {
		return err
	}
	unixTransport.RegisterLocation("restapi", addr)
	c.transport.RegisterProtocol(httpunix.Scheme, unixTransport)
	c.net = httpunix.Scheme
	c.hostname = "restapi"
	return nil
}
