package autonat

import (
	"context"
	"fmt"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	pb "github.com/libp2p/go-libp2p/p2p/host/autonat/pb"

	"github.com/libp2p/go-msgio/protoio"
	ma "github.com/multiformats/go-multiaddr"
)

// NewAutoNATClient creates a fresh instance of an AutoNATClient
// If addrFunc is nil, h.Addrs will be used
func NewAutoNATClient(h host.Host, addrFunc AddrFunc) Client {
	if addrFunc == nil {
		addrFunc = h.Addrs
	}
	return &client{h: h, addrFunc: addrFunc}
}

type client struct {
	h        host.Host
	addrFunc AddrFunc
}

// DialBack asks peer p to dial us back on all addresses returned by the addrFunc.
// It blocks until we've received a response from the peer.
//
// Note: A returned error Message_E_DIAL_ERROR does not imply that the server
// actually performed a dial attempt. Servers that run a version < v0.20.0 also
// return Message_E_DIAL_ERROR if the dial was skipped due to the dialPolicy.
func (c *client) DialBack(ctx context.Context, p peer.ID) (ma.Multiaddr, error) {
	s, err := c.h.NewStream(ctx, p, AutoNATProto)
	if err != nil {
		return nil, err
	}

	if err := s.Scope().SetService(ServiceName); err != nil {
		log.Debugf("error attaching stream to autonat service: %s", err)
		s.Reset()
		return nil, err
	}

	if err := s.Scope().ReserveMemory(maxMsgSize, network.ReservationPriorityAlways); err != nil {
		log.Debugf("error reserving memory for autonat stream: %s", err)
		s.Reset()
		return nil, err
	}
	defer s.Scope().ReleaseMemory(maxMsgSize)

	s.SetDeadline(time.Now().Add(streamTimeout))
	// Might as well just reset the stream. Once we get to this point, we
	// don't care about being nice.
	defer s.Close()

	r := protoio.NewDelimitedReader(s, maxMsgSize)
	w := protoio.NewDelimitedWriter(s)

	req := newDialMessage(peer.AddrInfo{ID: c.h.ID(), Addrs: c.addrFunc()})
	if err := w.WriteMsg(req); err != nil {
		s.Reset()
		return nil, err
	}

	var res pb.Message
	if err := r.ReadMsg(&res); err != nil {
		s.Reset()
		return nil, err
	}
	if res.GetType() != pb.Message_DIAL_RESPONSE {
		s.Reset()
		return nil, fmt.Errorf("unexpected response: %s", res.GetType().String())
	}

	status := res.GetDialResponse().GetStatus()
	switch status {
	case pb.Message_OK:
		addr := res.GetDialResponse().GetAddr()
		return ma.NewMultiaddrBytes(addr)
	default:
		return nil, Error{Status: status, Text: res.GetDialResponse().GetStatusText()}
	}
}

// Error wraps errors signalled by AutoNAT services
type Error struct {
	Status pb.Message_ResponseStatus
	Text   string
}

func (e Error) Error() string {
	return fmt.Sprintf("AutoNAT error: %s (%s)", e.Text, e.Status.String())
}

// IsDialError returns true if the error was due to a dial back failure
func (e Error) IsDialError() bool {
	return e.Status == pb.Message_E_DIAL_ERROR
}

// IsDialRefused returns true if the error was due to a refusal to dial back
func (e Error) IsDialRefused() bool {
	return e.Status == pb.Message_E_DIAL_REFUSED
}

// IsDialError returns true if the AutoNAT peer signalled an error dialing back
func IsDialError(e error) bool {
	ae, ok := e.(Error)
	return ok && ae.IsDialError()
}

// IsDialRefused returns true if the AutoNAT peer signalled refusal to dial back
func IsDialRefused(e error) bool {
	ae, ok := e.(Error)
	return ok && ae.IsDialRefused()
}
