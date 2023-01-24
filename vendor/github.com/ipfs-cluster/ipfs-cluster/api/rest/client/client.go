// Package client provides a Go Client for the IPFS Cluster API provided
// by the "api/rest" component. It supports both the HTTP(s) endpoint and
// the libp2p-http endpoint.
package client

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/ipfs-cluster/ipfs-cluster/api"

	shell "github.com/ipfs/go-ipfs-api"
	files "github.com/ipfs/go-ipfs-files"
	logging "github.com/ipfs/go-log/v2"
	host "github.com/libp2p/go-libp2p/core/host"
	peer "github.com/libp2p/go-libp2p/core/peer"
	pnet "github.com/libp2p/go-libp2p/core/pnet"
	ma "github.com/multiformats/go-multiaddr"
	madns "github.com/multiformats/go-multiaddr-dns"
	manet "github.com/multiformats/go-multiaddr/net"

	"go.opencensus.io/plugin/ochttp"
	"go.opencensus.io/plugin/ochttp/propagation/tracecontext"
	"go.opencensus.io/trace"
)

// Configuration defaults
var (
	DefaultTimeout   = 0
	DefaultAPIAddr   = "/ip4/127.0.0.1/tcp/9094"
	DefaultLogLevel  = "info"
	DefaultProxyPort = 9095
	ResolveTimeout   = 30 * time.Second
	DefaultPort      = 9094
)

var loggingFacility = "apiclient"
var logger = logging.Logger(loggingFacility)

// Client interface defines the interface to be used by API clients to
// interact with the ipfs-cluster-service. All methods take a
// context.Context as their first parameter, this allows for
// timing out and canceling of requests as well as recording
// metrics and tracing of requests through the API.
type Client interface {
	// ID returns information about the cluster Peer.
	ID(context.Context) (api.ID, error)

	// Peers requests ID information for all cluster peers.
	Peers(context.Context, chan<- api.ID) error
	// PeerAdd adds a new peer to the cluster.
	PeerAdd(ctx context.Context, pid peer.ID) (api.ID, error)
	// PeerRm removes a current peer from the cluster
	PeerRm(ctx context.Context, pid peer.ID) error

	// Add imports files to the cluster from the given paths.
	Add(ctx context.Context, paths []string, params api.AddParams, out chan<- api.AddedOutput) error
	// AddMultiFile imports new files from a MultiFileReader.
	AddMultiFile(ctx context.Context, multiFileR *files.MultiFileReader, params api.AddParams, out chan<- api.AddedOutput) error

	// Pin tracks a Cid with the given replication factor and a name for
	// human-friendliness.
	Pin(ctx context.Context, ci api.Cid, opts api.PinOptions) (api.Pin, error)
	// Unpin untracks a Cid from cluster.
	Unpin(ctx context.Context, ci api.Cid) (api.Pin, error)

	// PinPath resolves given path into a cid and performs the pin operation.
	PinPath(ctx context.Context, path string, opts api.PinOptions) (api.Pin, error)
	// UnpinPath resolves given path into a cid and performs the unpin operation.
	// It returns api.Pin of the given cid before it is unpinned.
	UnpinPath(ctx context.Context, path string) (api.Pin, error)

	// Allocations returns the consensus state listing all tracked items
	// and the peers that should be pinning them.
	Allocations(ctx context.Context, filter api.PinType, out chan<- api.Pin) error
	// Allocation returns the current allocations for a given Cid.
	Allocation(ctx context.Context, ci api.Cid) (api.Pin, error)

	// Status returns the current ipfs state for a given Cid. If local is true,
	// the information affects only the current peer, otherwise the information
	// is fetched from all cluster peers.
	Status(ctx context.Context, ci api.Cid, local bool) (api.GlobalPinInfo, error)
	// StatusCids status information for the requested CIDs.
	StatusCids(ctx context.Context, cids []api.Cid, local bool, out chan<- api.GlobalPinInfo) error
	// StatusAll gathers Status() for all tracked items.
	StatusAll(ctx context.Context, filter api.TrackerStatus, local bool, out chan<- api.GlobalPinInfo) error

	// Recover retriggers pin or unpin ipfs operations for a Cid in error
	// state.  If local is true, the operation is limited to the current
	// peer, otherwise it happens on every cluster peer.
	Recover(ctx context.Context, ci api.Cid, local bool) (api.GlobalPinInfo, error)
	// RecoverAll triggers Recover() operations on all tracked items. If
	// local is true, the operation is limited to the current peer.
	// Otherwise, it happens everywhere.
	RecoverAll(ctx context.Context, local bool, out chan<- api.GlobalPinInfo) error

	// Alerts returns information health events in the cluster (expired
	// metrics etc.).
	Alerts(ctx context.Context) ([]api.Alert, error)

	// Version returns the ipfs-cluster peer's version.
	Version(context.Context) (api.Version, error)

	// IPFS returns an instance of go-ipfs-api's Shell, pointing to a
	// Cluster's IPFS proxy endpoint.
	IPFS(context.Context) *shell.Shell

	// GetConnectGraph returns an ipfs-cluster connection graph.
	GetConnectGraph(context.Context) (api.ConnectGraph, error)

	// Metrics returns a map with the latest metrics of matching name
	// for the current cluster peers.
	Metrics(ctx context.Context, name string) ([]api.Metric, error)

	// MetricNames returns the list of metric types.
	MetricNames(ctx context.Context) ([]string, error)

	// RepoGC runs garbage collection on IPFS daemons of cluster peers and
	// returns collected CIDs. If local is true, it would garbage collect
	// only on contacted peer, otherwise on all peers' IPFS daemons.
	RepoGC(ctx context.Context, local bool) (api.GlobalRepoGC, error)
}

// Config allows to configure the parameters to connect
// to the ipfs-cluster REST API.
type Config struct {
	// Enable SSL support. Only valid without APIAddr.
	SSL bool
	// Skip certificate verification (insecure)
	NoVerifyCert bool

	// Username and password for basic authentication
	Username string
	Password string

	// The ipfs-cluster REST API endpoint in multiaddress form
	// (takes precedence over host:port). It this address contains
	// an /ipfs/, /p2p/ or /dnsaddr, the API will be contacted
	// through a libp2p tunnel, thus getting encryption for
	// free. Using the libp2p tunnel will ignore any configurations.
	APIAddr ma.Multiaddr

	// REST API endpoint host and port. Only valid without
	// APIAddr.
	Host string
	Port string

	// If APIAddr is provided, and the peer uses private networks (pnet),
	// then we need to provide the key. If the peer is the cluster peer,
	// this corresponds to the cluster secret.
	ProtectorKey pnet.PSK

	// ProxyAddr is used to obtain a go-ipfs-api Shell instance pointing
	// to the ipfs proxy endpoint of ipfs-cluster. If empty, the location
	// will be guessed from one of APIAddr/Host,
	// and the port used will be ipfs-cluster's proxy default port (9095)
	ProxyAddr ma.Multiaddr

	// Define timeout for network operations
	Timeout time.Duration

	// Specifies if we attempt to re-use connections to the same
	// hosts.
	DisableKeepAlives bool

	// LogLevel defines the verbosity of the logging facility
	LogLevel string
}

// AsTemplateFor creates client configs from resolved multiaddresses
func (c *Config) AsTemplateFor(addrs []ma.Multiaddr) []*Config {
	var cfgs []*Config
	for _, addr := range addrs {
		cfg := *c
		cfg.APIAddr = addr
		cfgs = append(cfgs, &cfg)
	}
	return cfgs
}

// AsTemplateForResolvedAddress creates client configs from a multiaddress
func (c *Config) AsTemplateForResolvedAddress(ctx context.Context, addr ma.Multiaddr) ([]*Config, error) {
	resolvedAddrs, err := resolveAddr(ctx, addr)
	if err != nil {
		return nil, err
	}
	return c.AsTemplateFor(resolvedAddrs), nil
}

// DefaultClient provides methods to interact with the ipfs-cluster API. Use
// NewDefaultClient() to create one.
type defaultClient struct {
	ctx       context.Context
	cancel    context.CancelFunc
	config    *Config
	transport *http.Transport
	net       string
	hostname  string
	client    *http.Client
	p2p       host.Host
}

// NewDefaultClient initializes a client given a Config.
func NewDefaultClient(cfg *Config) (Client, error) {
	ctx, cancel := context.WithCancel(context.Background())
	client := &defaultClient{
		ctx:    ctx,
		cancel: cancel,
		config: cfg,
	}

	if client.config.Port == "" {
		client.config.Port = fmt.Sprintf("%d", DefaultPort)
	}

	err := client.setupAPIAddr()
	if err != nil {
		return nil, err
	}

	err = client.resolveAPIAddr()
	if err != nil {
		return nil, err
	}

	err = client.setupHTTPClient()
	if err != nil {
		return nil, err
	}

	err = client.setupHostname()
	if err != nil {
		return nil, err
	}

	err = client.setupProxy()
	if err != nil {
		return nil, err
	}

	if lvl := cfg.LogLevel; lvl != "" {
		logging.SetLogLevel(loggingFacility, lvl)
	} else {
		logging.SetLogLevel(loggingFacility, DefaultLogLevel)
	}

	return client, nil
}

func (c *defaultClient) setupAPIAddr() error {
	if c.config.APIAddr != nil {
		return nil // already setup by user
	}

	var addr ma.Multiaddr
	var err error

	if c.config.Host == "" { //default
		addr, err := ma.NewMultiaddr(DefaultAPIAddr)
		c.config.APIAddr = addr
		return err
	}

	var addrStr string
	ip := net.ParseIP(c.config.Host)
	switch {
	case ip == nil:
		addrStr = fmt.Sprintf("/dns4/%s/tcp/%s", c.config.Host, c.config.Port)
	case ip.To4() != nil:
		addrStr = fmt.Sprintf("/ip4/%s/tcp/%s", c.config.Host, c.config.Port)
	default:
		addrStr = fmt.Sprintf("/ip6/%s/tcp/%s", c.config.Host, c.config.Port)
	}

	addr, err = ma.NewMultiaddr(addrStr)
	c.config.APIAddr = addr
	return err
}

func (c *defaultClient) resolveAPIAddr() error {
	// Only resolve libp2p addresses. For HTTP addresses, we let
	// the default client handle any resolving. We extract the hostname
	// in setupHostname()
	if !IsPeerAddress(c.config.APIAddr) {
		return nil
	}
	resolved, err := resolveAddr(c.ctx, c.config.APIAddr)
	if err != nil {
		return err
	}
	c.config.APIAddr = resolved[0]
	return nil
}

func (c *defaultClient) setupHTTPClient() error {
	var err error

	switch {
	case IsPeerAddress(c.config.APIAddr):
		err = c.enableLibp2p()
	case isUnixSocketAddress(c.config.APIAddr):
		err = c.enableUnix()
	case c.config.SSL:
		err = c.enableTLS()
	default:
		c.defaultTransport()
	}

	if err != nil {
		return err
	}

	c.client = &http.Client{
		Transport: &ochttp.Transport{
			Base:           c.transport,
			Propagation:    &tracecontext.HTTPFormat{},
			StartOptions:   trace.StartOptions{SpanKind: trace.SpanKindClient},
			FormatSpanName: func(req *http.Request) string { return req.Host + ":" + req.URL.Path + ":" + req.Method },
			NewClientTrace: ochttp.NewSpanAnnotatingClientTrace,
		},
		Timeout: c.config.Timeout,
	}
	return nil
}

func (c *defaultClient) setupHostname() error {
	// Extract host:port form APIAddr or use Host:Port.
	// For libp2p, hostname is set in enableLibp2p()
	// For unix sockets, hostname set in enableUnix()
	if IsPeerAddress(c.config.APIAddr) || isUnixSocketAddress(c.config.APIAddr) {
		return nil
	}
	_, hostname, err := manet.DialArgs(c.config.APIAddr)
	if err != nil {
		return err
	}

	c.hostname = hostname
	return nil
}

func (c *defaultClient) setupProxy() error {
	if c.config.ProxyAddr != nil {
		return nil
	}

	// Guess location from	APIAddr
	port, err := ma.NewMultiaddr(fmt.Sprintf("/tcp/%d", DefaultProxyPort))
	if err != nil {
		return err
	}
	c.config.ProxyAddr = ma.Split(c.config.APIAddr)[0].Encapsulate(port)
	return nil
}

// IPFS returns an instance of go-ipfs-api's Shell, pointing to the
// configured ProxyAddr (or to the default Cluster's IPFS proxy port).
// It re-uses this Client's HTTP client, thus will be constrained by
// the same configurations affecting it (timeouts...).
func (c *defaultClient) IPFS(ctx context.Context) *shell.Shell {
	return shell.NewShellWithClient(c.config.ProxyAddr.String(), c.client)
}

// IsPeerAddress detects if the given multiaddress identifies a libp2p peer,
// either because it has the /p2p/ protocol or because it uses /dnsaddr/
func IsPeerAddress(addr ma.Multiaddr) bool {
	if addr == nil {
		return false
	}
	pid, err := addr.ValueForProtocol(ma.P_P2P)
	dnsaddr, err2 := addr.ValueForProtocol(ma.P_DNSADDR)
	return (pid != "" && err == nil) || (dnsaddr != "" && err2 == nil)
}

// isUnixSocketAddress returns if the given address corresponds to a
// unix socket.
func isUnixSocketAddress(addr ma.Multiaddr) bool {
	if addr == nil {
		return false
	}
	value, err := addr.ValueForProtocol(ma.P_UNIX)
	return (value != "" && err == nil)
}

// resolve addr
func resolveAddr(ctx context.Context, addr ma.Multiaddr) ([]ma.Multiaddr, error) {
	resolveCtx, cancel := context.WithTimeout(ctx, ResolveTimeout)
	defer cancel()
	resolved, err := madns.Resolve(resolveCtx, addr)
	if err != nil {
		return nil, err
	}

	if len(resolved) == 0 {
		return nil, fmt.Errorf("resolving %s returned 0 results", addr)
	}

	return resolved, nil
}
