package client

import (
	"context"
	"fmt"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	pbv1 "github.com/libp2p/go-libp2p/p2p/protocol/circuitv1/pb"
	pbv2 "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/pb"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/proto"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/util"

	ma "github.com/multiformats/go-multiaddr"
)

const maxMessageSize = 4096

var DialTimeout = time.Minute
var DialRelayTimeout = 5 * time.Second

// relay protocol errors; used for signalling deduplication
type relayError struct {
	err string
}

func (e relayError) Error() string {
	return e.err
}

func newRelayError(t string, args ...interface{}) error {
	return relayError{err: fmt.Sprintf(t, args...)}
}

func isRelayError(err error) bool {
	_, ok := err.(relayError)
	return ok
}

// dialer
func (c *Client) dial(ctx context.Context, a ma.Multiaddr, p peer.ID) (*Conn, error) {
	// split /a/p2p-circuit/b into (/a, /p2p-circuit/b)
	relayaddr, destaddr := ma.SplitFunc(a, func(c ma.Component) bool {
		return c.Protocol().Code == ma.P_CIRCUIT
	})

	// If the address contained no /p2p-circuit part, the second part is nil.
	if destaddr == nil {
		return nil, fmt.Errorf("%s is not a relay address", a)
	}

	if relayaddr == nil {
		return nil, fmt.Errorf("can't dial a p2p-circuit without specifying a relay: %s", a)
	}

	dinfo := peer.AddrInfo{ID: p}

	// Strip the /p2p-circuit prefix from the destaddr so that we can pass the destination address
	// (if present) for active relays
	_, destaddr = ma.SplitFirst(destaddr)
	if destaddr != nil {
		dinfo.Addrs = append(dinfo.Addrs, destaddr)
	}

	rinfo, err := peer.AddrInfoFromP2pAddr(relayaddr)
	if err != nil {
		return nil, fmt.Errorf("error parsing relay multiaddr '%s': %w", relayaddr, err)
	}

	// deduplicate active relay dials to the same peer
retry:
	c.mx.Lock()
	dedup, active := c.activeDials[p]
	if !active {
		dedup = &completion{ch: make(chan struct{}), relay: rinfo.ID}
		c.activeDials[p] = dedup
	}
	c.mx.Unlock()

	if active {
		select {
		case <-dedup.ch:
			if dedup.err != nil {
				if dedup.relay != rinfo.ID {
					// different relay, retry
					goto retry
				}

				if !isRelayError(dedup.err) {
					// not a relay protocol error, retry
					goto retry
				}

				// don't try the same relay if it failed to connect with a protocol error
				return nil, fmt.Errorf("concurrent active dial through the same relay failed with a protocol error")
			}

			return nil, fmt.Errorf("concurrent active dial succeeded")

		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	conn, err := c.dialPeer(ctx, *rinfo, dinfo)

	c.mx.Lock()
	dedup.err = err
	close(dedup.ch)
	delete(c.activeDials, p)
	c.mx.Unlock()

	return conn, err
}

func (c *Client) dialPeer(ctx context.Context, relay, dest peer.AddrInfo) (*Conn, error) {
	log.Debugf("dialing peer %s through relay %s", dest.ID, relay.ID)

	if len(relay.Addrs) > 0 {
		c.host.Peerstore().AddAddrs(relay.ID, relay.Addrs, peerstore.TempAddrTTL)
	}

	dialCtx, cancel := context.WithTimeout(ctx, DialRelayTimeout)
	defer cancel()
	s, err := c.host.NewStream(dialCtx, relay.ID, proto.ProtoIDv2Hop, proto.ProtoIDv1)
	if err != nil {
		return nil, fmt.Errorf("error opening hop stream to relay: %w", err)
	}

	switch s.Protocol() {
	case proto.ProtoIDv2Hop:
		return c.connectV2(s, dest)

	case proto.ProtoIDv1:
		return c.connectV1(s, dest)

	default:
		s.Reset()
		return nil, fmt.Errorf("unexpected stream protocol: %s", s.Protocol())
	}
}

func (c *Client) connectV2(s network.Stream, dest peer.AddrInfo) (*Conn, error) {
	if err := s.Scope().ReserveMemory(maxMessageSize, network.ReservationPriorityAlways); err != nil {
		s.Reset()
		return nil, err
	}
	defer s.Scope().ReleaseMemory(maxMessageSize)

	rd := util.NewDelimitedReader(s, maxMessageSize)
	wr := util.NewDelimitedWriter(s)
	defer rd.Close()

	var msg pbv2.HopMessage

	msg.Type = pbv2.HopMessage_CONNECT.Enum()
	msg.Peer = util.PeerInfoToPeerV2(dest)

	s.SetDeadline(time.Now().Add(DialTimeout))

	err := wr.WriteMsg(&msg)
	if err != nil {
		s.Reset()
		return nil, err
	}

	msg.Reset()

	err = rd.ReadMsg(&msg)
	if err != nil {
		s.Reset()
		return nil, err
	}

	s.SetDeadline(time.Time{})

	if msg.GetType() != pbv2.HopMessage_STATUS {
		s.Reset()
		return nil, newRelayError("unexpected relay response; not a status message (%d)", msg.GetType())
	}

	status := msg.GetStatus()
	if status != pbv2.Status_OK {
		s.Reset()
		return nil, newRelayError("error opening relay circuit: %s (%d)", pbv2.Status_name[int32(status)], status)
	}

	// check for a limit provided by the relay; if the limit is not nil, then this is a limited
	// relay connection and we mark the connection as transient.
	var stat network.ConnStats
	if limit := msg.GetLimit(); limit != nil {
		stat.Transient = true
		stat.Extra = make(map[interface{}]interface{})
		stat.Extra[StatLimitDuration] = time.Duration(limit.GetDuration()) * time.Second
		stat.Extra[StatLimitData] = limit.GetData()
	}

	return &Conn{stream: s, remote: dest, stat: stat, client: c}, nil
}

func (c *Client) connectV1(s network.Stream, dest peer.AddrInfo) (*Conn, error) {
	if err := s.Scope().ReserveMemory(maxMessageSize, network.ReservationPriorityAlways); err != nil {
		s.Reset()
		return nil, err
	}
	defer s.Scope().ReleaseMemory(maxMessageSize)

	rd := util.NewDelimitedReader(s, maxMessageSize)
	wr := util.NewDelimitedWriter(s)
	defer rd.Close()

	var msg pbv1.CircuitRelay

	msg.Type = pbv1.CircuitRelay_HOP.Enum()
	msg.SrcPeer = util.PeerInfoToPeerV1(c.host.Peerstore().PeerInfo(c.host.ID()))
	msg.DstPeer = util.PeerInfoToPeerV1(dest)

	s.SetDeadline(time.Now().Add(DialTimeout))

	err := wr.WriteMsg(&msg)
	if err != nil {
		s.Reset()
		return nil, err
	}

	msg.Reset()

	err = rd.ReadMsg(&msg)
	if err != nil {
		s.Reset()
		return nil, err
	}

	s.SetDeadline(time.Time{})

	if msg.GetType() != pbv1.CircuitRelay_STATUS {
		s.Reset()
		return nil, newRelayError("unexpected relay response; not a status message (%d)", msg.GetType())
	}

	status := msg.GetCode()
	if status != pbv1.CircuitRelay_SUCCESS {
		s.Reset()
		return nil, newRelayError("error opening relay circuit: %s (%d)", pbv1.CircuitRelay_Status_name[int32(status)], status)
	}

	return &Conn{stream: s, remote: dest, client: c}, nil
}
