package client

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/pkg/errors"
)

// DummyHost is a hostname used for local communication.
//
// It acts as a valid formatted hostname for local connections (such as "unix://"
// or "npipe://") which do not require a hostname. It should never be resolved,
// but uses the special-purpose ".localhost" TLD (as defined in [RFC 2606, Section 2]
// and [RFC 6761, Section 6.3]).
//
// [RFC 7230, Section 5.4] defines that an empty header must be used for such
// cases:
//
//	If the authority component is missing or undefined for the target URI,
//	then a client MUST send a Host header field with an empty field-value.
//
// However, [Go stdlib] enforces the semantics of HTTP(S) over TCP, does not
// allow an empty header to be used, and requires req.URL.Scheme to be either
// "http" or "https".
//
// For further details, refer to:
//
//   - https://github.com/docker/engine-api/issues/189
//   - https://github.com/golang/go/issues/13624
//   - https://github.com/golang/go/issues/61076
//   - https://github.com/moby/moby/issues/45935
//
// [RFC 2606, Section 2]: https://www.rfc-editor.org/rfc/rfc2606.html#section-2
// [RFC 6761, Section 6.3]: https://www.rfc-editor.org/rfc/rfc6761#section-6.3
// [RFC 7230, Section 5.4]: https://datatracker.ietf.org/doc/html/rfc7230#section-5.4
// [Go stdlib]: https://github.com/golang/go/blob/6244b1946bc2101b01955468f1be502dbadd6807/src/net/http/transport.go#L558-L569
const DummyHost = "api.moby.localhost"

// DefaultVersion is the pinned version of the docker API we utilize.
const DefaultVersion = "1.47"

// Client is the API client that performs all operations
// against a docker server.
type Client struct {
	// scheme sets the scheme for the client
	scheme string
	// host holds the server address to connect to
	host string
	// proto holds the client protocol i.e. unix.
	proto string
	// addr holds the client address.
	addr string
	// basePath holds the path to prepend to the requests.
	basePath string
	// client used to send and receive http requests.
	client *http.Client
	// version of the server to talk to.
	version string

	// When the client transport is an *http.Transport (default) we need to do some extra things (like closing idle connections).
	// Store the original transport as the http.Client transport will be wrapped with tracing libs.
	baseTransport *http.Transport
}

// ErrRedirect is the error returned by checkRedirect when the request is non-GET.
var ErrRedirect = errors.New("unexpected redirect in response")

// CheckRedirect specifies the policy for dealing with redirect responses. It
// can be set on [http.Client.CheckRedirect] to prevent HTTP redirects for
// non-GET requests. It returns an [ErrRedirect] for non-GET request, otherwise
// returns a [http.ErrUseLastResponse], which is special-cased by http.Client
// to use the last response.
//
// Go 1.8 changed behavior for HTTP redirects (specifically 301, 307, and 308)
// in the client. The client (and by extension API client) can be made to send
// a request like "POST /containers//start" where what would normally be in the
// name section of the URL is empty. This triggers an HTTP 301 from the daemon.
//
// In go 1.8 this 301 is converted to a GET request, and ends up getting
// a 404 from the daemon. This behavior change manifests in the client in that
// before, the 301 was not followed and the client did not generate an error,
// but now results in a message like "Error response from daemon: page not found".
func CheckRedirect(_ *http.Request, via []*http.Request) error {
	if via[0].Method == http.MethodGet {
		return http.ErrUseLastResponse
	}
	return ErrRedirect
}

// NewClientWithOpts initializes a new API client with a default HTTPClient, and
// default API host and version. It also initializes the custom HTTP headers to
// add to each request.
//
// It takes an optional list of [Opt] functional arguments, which are applied in
// the order they're provided, which allows modifying the defaults when creating
// the client. For example, the following initializes a client that configures
// itself with values from environment variables ([FromEnv]), and has automatic
// API version negotiation enabled ([WithAPIVersionNegotiation]).
//
//	cli, err := client.NewClientWithOpts(
//		client.FromEnv,
//		client.WithAPIVersionNegotiation(),
//	)
func NewClientWithOpts(ops ...Opt) (*Client, error) {
	hostURL, err := ParseHostURL(DefaultDockerHost)
	if err != nil {
		return nil, err
	}

	client, err := defaultHTTPClient(hostURL)
	if err != nil {
		return nil, err
	}
	c := &Client{
		host:    DefaultDockerHost,
		version: DefaultVersion,
		client:  client,
		proto:   hostURL.Scheme,
		addr:    hostURL.Host,
	}

	for _, op := range ops {
		if err := op(c); err != nil {
			return nil, err
		}
	}

	if tr, ok := c.client.Transport.(*http.Transport); ok {
		// Store the base transport before we wrap it in tracing libs below
		// This is used, as an example, to close idle connections when the client is closed
		c.baseTransport = tr
	}

	if c.scheme == "" {
		// TODO(stevvooe): This isn't really the right way to write clients in Go.
		// `NewClient` should probably only take an `*http.Client` and work from there.
		// Unfortunately, the model of having a host-ish/url-thingy as the connection
		// string has us confusing protocol and transport layers. We continue doing
		// this to avoid breaking existing clients but this should be addressed.
		if c.tlsConfig() != nil {
			c.scheme = "https"
		} else {
			c.scheme = "http"
		}
	}
	return c, nil
}

func (cli *Client) tlsConfig() *tls.Config {
	if cli.baseTransport == nil {
		return nil
	}
	return cli.baseTransport.TLSClientConfig
}

func defaultHTTPClient(hostURL *url.URL) (*http.Client, error) {
	// Necessary to prevent long-lived processes using the
	// client from leaking connections due to idle connections
	// not being released.
	transport := &http.Transport{
		MaxIdleConns:    6,
		IdleConnTimeout: 30 * time.Second,
	}
	if err := configureTransport(transport, hostURL.Scheme, hostURL.Host); err != nil {
		return nil, err
	}
	return &http.Client{
		Transport:     transport,
		CheckRedirect: CheckRedirect,
	}, nil
}

// Close the transport used by the client
func (cli *Client) Close() error {
	if cli.baseTransport != nil {
		cli.baseTransport.CloseIdleConnections()
		return nil
	}
	return nil
}

// ParseHostURL parses a url string, validates the string is a host url, and
// returns the parsed URL
func ParseHostURL(host string) (*url.URL, error) {
	proto, addr, ok := strings.Cut(host, "://")
	if !ok || addr == "" {
		return nil, errors.Errorf("unable to parse docker host `%s`", host)
	}

	var basePath string
	if proto == "tcp" {
		parsed, err := url.Parse("tcp://" + addr)
		if err != nil {
			return nil, err
		}
		addr = parsed.Host
		basePath = parsed.Path
	}
	return &url.URL{
		Scheme: proto,
		Host:   addr,
		Path:   basePath,
	}, nil
}

func (cli *Client) dialerFromTransport() func(context.Context, string, string) (net.Conn, error) {
	if cli.baseTransport == nil || cli.baseTransport.DialContext == nil {
		return nil
	}

	if cli.baseTransport.TLSClientConfig != nil {
		// When using a tls config we don't use the configured dialer but instead a fallback dialer...
		// Note: It seems like this should use the normal dialer and wrap the returned net.Conn in a tls.Conn
		// I honestly don't know why it doesn't do that, but it doesn't and such a change is entirely unrelated to the change in this commit.
		return nil
	}
	return cli.baseTransport.DialContext
}

// Dialer returns a dialer for a raw stream connection, with an HTTP/1.1 header,
// that can be used for proxying the daemon connection. It is used by
// ["docker dial-stdio"].
//
// ["docker dial-stdio"]: https://github.com/docker/cli/pull/1014
func (cli *Client) Dialer() func(context.Context) (net.Conn, error) {
	return func(ctx context.Context) (net.Conn, error) {
		if dialFn := cli.dialerFromTransport(); dialFn != nil {
			return dialFn(ctx, cli.proto, cli.addr)
		}
		switch cli.proto {
		case "unix":
			return net.Dial(cli.proto, cli.addr)
		case "npipe":
			return DialPipe(cli.addr, 32*time.Second)
		default:
			if tlsConfig := cli.tlsConfig(); tlsConfig != nil {
				return tls.Dial(cli.proto, cli.addr, tlsConfig)
			}
			return net.Dial(cli.proto, cli.addr)
		}
	}
}
