package relay

import (
	"github.com/libp2p/go-libp2p/core/peer"
)

type Resources struct {
	// MaxCircuits is the maximum number of active relay connections
	MaxCircuits int

	// MaxCircuitsPerPeer is the maximum number of active relay connections per peer
	MaxCircuitsPerPeer int

	// BufferSize is the buffer size for relaying in each direction
	BufferSize int
}

func DefaultResources() Resources {
	return Resources{
		MaxCircuits:        1024,
		MaxCircuitsPerPeer: 64,
		BufferSize:         4096,
	}
}

type ACLFilter interface {
	AllowHop(src, dest peer.ID) bool
}

type Option func(r *Relay) error

// WithResources specifies resource limits for the relay
func WithResources(rc Resources) Option {
	return func(r *Relay) error {
		r.rc = rc
		return nil
	}
}

// WithACL specifies an ACLFilter for access control
func WithACL(acl ACLFilter) Option {
	return func(r *Relay) error {
		r.acl = acl
		return nil
	}
}
