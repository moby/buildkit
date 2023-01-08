// Package p2phttp allows to serve HTTP endpoints and make HTTP requests through
// LibP2P (https://github.com/libp2p/libp2p) using Go's standard "http" and
// "net" stacks.
//
// Instead of the regular "host:port" addressing, `p2phttp` uses a Peer ID
// and lets LibP2P take care of the routing, thus taking advantage
// of features like multi-routes,  NAT transversal and stream multiplexing
// over a single connection.
//
// When already running a LibP2P facility, this package allows to expose
// existing HTTP-based services (like REST APIs) through LibP2P and to
// use those services with minimal changes to the code-base.
//
// For example, a simple http.Server on LibP2P works as:
//
//		listener, _ := gostream.Listen(host1, p2phttp.DefaultP2PProtocol)
//		defer listener.Close()
//		go func() {
//			http.HandleFunc("/hello", func(w http.ResponseWriter, r *http.Request) {
//				w.Write([]byte("Hi!"))
//			})
//			server := &http.Server{}
//			server.Serve(listener)
//		}
//	     ...
//
// As shown above, a Server only needs a
// "github.com/libp2p/go-libp2p-gostream" listener. This listener will
// use a libP2P host to watch for stream tagged with our Protocol.
//
// On the other side, a client just needs to be initialized with a custom
// LibP2P host-based transport to perform requests to such server:
//
//	tr := &http.Transport{}
//	tr.RegisterProtocol("libp2p", p2phttp.NewTransport(clientHost))
//	client := &http.Client{Transport: tr}
//	res, err := client.Get("libp2p://Qmaoi4isbcTbFfohQyn28EiYM5CDWQx9QRCjDh3CTeiY7P/hello")
//	...
//
// In the example above, the client registers a "libp2p" protocol for which the
// custom transport is used. It can still perform regular "http" requests. The
// protocol name used is arbitraty and non standard.
//
// Note that LibP2P hosts cannot dial to themselves, so there is no possibility
// of using the same host as server and as client.
package p2phttp

import (
	"bufio"
	"io"
	"net"
	"net/http"

	gostream "github.com/libp2p/go-libp2p-gostream"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

// DefaultP2PProtocol is used to tag and identify streams
// handled by go-libp2p-http
var DefaultP2PProtocol protocol.ID = "/libp2p-http"

// options holds configuration options for the transport.
type options struct {
	Protocol protocol.ID
}

// Option allows to set the libp2p transport options.
type Option func(o *options)

// ProtocolOption sets the Protocol Tag associated to the libp2p roundtripper.
func ProtocolOption(p protocol.ID) Option {
	return func(o *options) {
		o.Protocol = p
	}
}

// RoundTripper implemenets http.RoundTrip and can be used as
// custom transport with Go http.Client.
type RoundTripper struct {
	h    host.Host
	opts options
}

// NewTransport returns a new RoundTripper which uses the provided
// libP2P host to perform an http request and obtain the response.
//
// The typical use case for NewTransport is to register the "libp2p"
// protocol with a Transport, as in:
//
//	t := &http.Transport{}
//	t.RegisterProtocol("libp2p", p2phttp.NewTransport(host, ProtocolOption(DefaultP2PProtocol)))
//	c := &http.Client{Transport: t}
//	res, err := c.Get("libp2p://Qmaoi4isbcTbFfohQyn28EiYM5CDWQx9QRCjDh3CTeiY7P/index.html")
//	...
func NewTransport(h host.Host, opts ...Option) *RoundTripper {
	defOpts := options{
		Protocol: DefaultP2PProtocol,
	}
	for _, o := range opts {
		o(&defOpts)
	}

	return &RoundTripper{h, defOpts}
}

// we wrap the response body and close the stream
// only when it's closed.
type respBody struct {
	io.ReadCloser
	conn net.Conn
}

// Closes the response's body and the connection.
func (rb *respBody) Close() error {
	rb.conn.Close()
	return rb.ReadCloser.Close()
}

// RoundTrip executes a single HTTP transaction, returning
// a Response for the provided Request.
func (rt *RoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	addr := r.Host
	if addr == "" {
		addr = r.URL.Host
	}

	pid, err := peer.Decode(addr)
	if err != nil {
		return nil, err
	}

	conn, err := gostream.Dial(r.Context(), rt.h, peer.ID(pid), rt.opts.Protocol)
	if err != nil {
		if r.Body != nil {
			r.Body.Close()
		}
		return nil, err
	}

	// Write the request while reading the response
	go func() {
		err := r.Write(conn)
		if err != nil {
			conn.Close()
		}
		if r.Body != nil {
			r.Body.Close()
		}
	}()

	resp, err := http.ReadResponse(bufio.NewReader(conn), r)
	if err != nil {
		return resp, err
	}

	resp.Body = &respBody{
		ReadCloser: resp.Body,
		conn:       conn,
	}

	return resp, nil
}
