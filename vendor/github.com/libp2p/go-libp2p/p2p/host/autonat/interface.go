package autonat

import (
	"context"
	"io"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	ma "github.com/multiformats/go-multiaddr"
)

// AutoNAT is the interface for NAT autodiscovery
type AutoNAT interface {
	// Status returns the current NAT status
	Status() network.Reachability
	// PublicAddr returns the public dial address when NAT status is public and an
	// error otherwise
	PublicAddr() (ma.Multiaddr, error)
	io.Closer
}

// Client is a stateless client interface to AutoNAT peers
type Client interface {
	// DialBack requests from a peer providing AutoNAT services to test dial back
	// and report the address on a successful connection.
	DialBack(ctx context.Context, p peer.ID) (ma.Multiaddr, error)
}

// AddrFunc is a function returning the candidate addresses for the local host.
type AddrFunc func() []ma.Multiaddr

// Option is an Autonat option for configuration
type Option func(*config) error
