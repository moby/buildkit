package noise

import (
	"context"
	"net"

	"github.com/libp2p/go-libp2p/core/canonicallog"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/sec"
	manet "github.com/multiformats/go-multiaddr/net"
)

type SessionOption = func(*SessionTransport) error

var _ sec.SecureTransport = &SessionTransport{}

// SessionTransport can be used
// to provide per-connection options
type SessionTransport struct {
	t *Transport
	// options
	prologue []byte
}

// SecureInbound runs the Noise handshake as the responder.
// If p is empty, connections from any peer are accepted.
func (i *SessionTransport) SecureInbound(ctx context.Context, insecure net.Conn, p peer.ID) (sec.SecureConn, error) {
	c, err := newSecureSession(i.t, ctx, insecure, p, i.prologue, false)
	if err != nil {
		addr, maErr := manet.FromNetAddr(insecure.RemoteAddr())
		if maErr == nil {
			canonicallog.LogPeerStatus(100, p, addr, "handshake_failure", "noise", "err", err.Error())
		}
	}
	return c, err
}

// SecureOutbound runs the Noise handshake as the initiator.
func (i *SessionTransport) SecureOutbound(ctx context.Context, insecure net.Conn, p peer.ID) (sec.SecureConn, error) {
	return newSecureSession(i.t, ctx, insecure, p, i.prologue, true)
}

func (t *Transport) WithSessionOptions(opts ...SessionOption) (sec.SecureTransport, error) {
	st := &SessionTransport{t: t}
	for _, opt := range opts {
		if err := opt(st); err != nil {
			return nil, err
		}
	}
	return st, nil
}

func Prologue(prologue []byte) SessionOption {
	return func(s *SessionTransport) error {
		s.prologue = prologue
		return nil
	}
}
