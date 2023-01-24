package websocket

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"

	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

var (
	wsma  = ma.StringCast("/ws")
	wssma = ma.StringCast("/wss")
)

type listener struct {
	nl     net.Listener
	server http.Server

	laddr ma.Multiaddr

	closed   chan struct{}
	incoming chan *Conn
}

// newListener creates a new listener from a raw net.Listener.
// tlsConf may be nil (for unencrypted websockets).
func newListener(a ma.Multiaddr, tlsConf *tls.Config) (*listener, error) {
	// Only look at the _last_ component.
	maddr, wscomponent := ma.SplitLast(a)
	isWSS := wscomponent.Equal(wssma)
	if isWSS && tlsConf == nil {
		return nil, fmt.Errorf("cannot listen on wss address %s without a tls.Config", a)
	}
	lnet, lnaddr, err := manet.DialArgs(maddr)
	if err != nil {
		return nil, err
	}
	nl, err := net.Listen(lnet, lnaddr)
	if err != nil {
		return nil, err
	}

	laddr, err := manet.FromNetAddr(nl.Addr())
	if err != nil {
		return nil, err
	}
	first, _ := ma.SplitFirst(a)
	// Don't resolve dns addresses.
	// We want to be able to announce domain names, so the peer can validate the TLS certificate.
	if c := first.Protocol().Code; c == ma.P_DNS || c == ma.P_DNS4 || c == ma.P_DNS6 || c == ma.P_DNSADDR {
		_, last := ma.SplitFirst(laddr)
		laddr = first.Encapsulate(last)
	}

	ln := &listener{
		nl:       nl,
		laddr:    laddr.Encapsulate(wscomponent),
		incoming: make(chan *Conn),
		closed:   make(chan struct{}),
	}
	ln.server = http.Server{Handler: ln}
	if isWSS {
		ln.server.TLSConfig = tlsConf
	}
	return ln, nil
}

func (l *listener) serve() {
	defer close(l.closed)
	if l.server.TLSConfig == nil {
		l.server.Serve(l.nl)
	} else {
		l.server.ServeTLS(l.nl, "", "")
	}
}

func (l *listener) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		// The upgrader writes a response for us.
		return
	}

	select {
	case l.incoming <- NewConn(c, false):
	case <-l.closed:
		c.Close()
	}
	// The connection has been hijacked, it's safe to return.
}

func (l *listener) Accept() (manet.Conn, error) {
	select {
	case c, ok := <-l.incoming:
		if !ok {
			return nil, fmt.Errorf("listener is closed")
		}

		mnc, err := manet.WrapNetConn(c)
		if err != nil {
			c.Close()
			return nil, err
		}

		return mnc, nil
	case <-l.closed:
		return nil, fmt.Errorf("listener is closed")
	}
}

func (l *listener) Addr() net.Addr {
	return l.nl.Addr()
}

func (l *listener) Close() error {
	l.server.Close()
	err := l.nl.Close()
	<-l.closed
	return err
}

func (l *listener) Multiaddr() ma.Multiaddr {
	return l.laddr
}
