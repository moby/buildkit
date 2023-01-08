package client

import (
	"context"
	"io"
	"sync"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/transport"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/proto"

	logging "github.com/ipfs/go-log/v2"
)

var log = logging.Logger("p2p-circuit")

// Client implements the client-side of the p2p-circuit/v2 protocol:
// - it implements dialing through v2 relays
// - it listens for incoming connections through v2 relays.
//
// For backwards compatibility with v1 relays and older nodes, the client will
// also accept relay connections through v1 relays and fallback dial peers using p2p-circuit/v1.
// This allows us to use the v2 code as drop in replacement for v1 in a host without breaking
// existing code and interoperability with older nodes.
type Client struct {
	ctx       context.Context
	ctxCancel context.CancelFunc
	host      host.Host
	upgrader  transport.Upgrader

	incoming chan accept

	mx          sync.Mutex
	activeDials map[peer.ID]*completion
	hopCount    map[peer.ID]int
}

var _ io.Closer = &Client{}
var _ transport.Transport = &Client{}

type accept struct {
	conn          *Conn
	writeResponse func() error
}

type completion struct {
	ch    chan struct{}
	relay peer.ID
	err   error
}

// New constructs a new p2p-circuit/v2 client, attached to the given host and using the given
// upgrader to perform connection upgrades.
func New(h host.Host, upgrader transport.Upgrader) (*Client, error) {
	cl := &Client{
		host:        h,
		upgrader:    upgrader,
		incoming:    make(chan accept),
		activeDials: make(map[peer.ID]*completion),
		hopCount:    make(map[peer.ID]int),
	}
	cl.ctx, cl.ctxCancel = context.WithCancel(context.Background())
	return cl, nil
}

// Start registers the circuit (client) protocol stream handlers
func (c *Client) Start() {
	c.host.SetStreamHandler(proto.ProtoIDv1, c.handleStreamV1)
	c.host.SetStreamHandler(proto.ProtoIDv2Stop, c.handleStreamV2)
}

func (c *Client) Close() error {
	c.ctxCancel()
	c.host.RemoveStreamHandler(proto.ProtoIDv1)
	c.host.RemoveStreamHandler(proto.ProtoIDv2Stop)
	return nil
}
