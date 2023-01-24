package holepunch

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	pb "github.com/libp2p/go-libp2p/p2p/protocol/holepunch/pb"
	"github.com/libp2p/go-libp2p/p2p/protocol/identify"

	logging "github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-msgio/protoio"
	ma "github.com/multiformats/go-multiaddr"
)

// Protocol is the libp2p protocol for Hole Punching.
const Protocol protocol.ID = "/libp2p/dcutr"

var log = logging.Logger("p2p-holepunch")

// StreamTimeout is the timeout for the hole punch protocol stream.
var StreamTimeout = 1 * time.Minute

const (
	ServiceName = "libp2p.holepunch"

	maxMsgSize = 4 * 1024 // 4K
)

// ErrClosed is returned when the hole punching is closed
var ErrClosed = errors.New("hole punching service closing")

type Option func(*Service) error

// The Service runs on every node that supports the DCUtR protocol.
type Service struct {
	ctx       context.Context
	ctxCancel context.CancelFunc

	host host.Host
	ids  identify.IDService

	holePuncherMx sync.Mutex
	holePuncher   *holePuncher

	hasPublicAddrsChan chan struct{}

	tracer *tracer

	refCount sync.WaitGroup
}

// NewService creates a new service that can be used for hole punching
// The Service runs on all hosts that support the DCUtR protocol,
// no matter if they are behind a NAT / firewall or not.
// The Service handles DCUtR streams (which are initiated from the node behind
// a NAT / Firewall once we establish a connection to them through a relay.
func NewService(h host.Host, ids identify.IDService, opts ...Option) (*Service, error) {
	if ids == nil {
		return nil, errors.New("identify service can't be nil")
	}

	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{
		ctx:                ctx,
		ctxCancel:          cancel,
		host:               h,
		ids:                ids,
		hasPublicAddrsChan: make(chan struct{}),
	}

	for _, opt := range opts {
		if err := opt(s); err != nil {
			cancel()
			return nil, err
		}
	}

	s.refCount.Add(1)
	go s.watchForPublicAddr()

	return s, nil
}

func (s *Service) watchForPublicAddr() {
	defer s.refCount.Done()

	log.Debug("waiting until we have at least one public address", "peer", s.host.ID())

	// TODO: We should have an event here that fires when identify discovers a new
	// address (and when autonat confirms that address).
	// As we currently don't have an event like this, just check our observed addresses
	// regularly (exponential backoff starting at 250 ms, capped at 5s).
	duration := 250 * time.Millisecond
	const maxDuration = 5 * time.Second
	t := time.NewTimer(duration)
	defer t.Stop()
	for {
		if containsPublicAddr(s.ids.OwnObservedAddrs()) {
			log.Debug("Host now has a public address. Starting holepunch protocol.")
			s.host.SetStreamHandler(Protocol, s.handleNewStream)
			break
		}

		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
			duration *= 2
			if duration > maxDuration {
				duration = maxDuration
			}
			t.Reset(duration)
		}
	}

	// Only start the holePuncher if we're behind a NAT / firewall.
	sub, err := s.host.EventBus().Subscribe(&event.EvtLocalReachabilityChanged{})
	if err != nil {
		log.Debugf("failed to subscripe to Reachability event: %s", err)
		return
	}
	defer sub.Close()
	for {
		select {
		case <-s.ctx.Done():
			return
		case e, ok := <-sub.Out():
			if !ok {
				return
			}
			if e.(event.EvtLocalReachabilityChanged).Reachability != network.ReachabilityPrivate {
				continue
			}
			s.holePuncherMx.Lock()
			s.holePuncher = newHolePuncher(s.host, s.ids, s.tracer)
			s.holePuncherMx.Unlock()
			close(s.hasPublicAddrsChan)
			return
		}
	}
}

// Close closes the Hole Punch Service.
func (s *Service) Close() error {
	var err error
	s.holePuncherMx.Lock()
	if s.holePuncher != nil {
		err = s.holePuncher.Close()
	}
	s.holePuncherMx.Unlock()
	s.tracer.Close()
	s.host.RemoveStreamHandler(Protocol)
	s.ctxCancel()
	s.refCount.Wait()
	return err
}

func (s *Service) incomingHolePunch(str network.Stream) (rtt time.Duration, addrs []ma.Multiaddr, err error) {
	// sanity check: a hole punch request should only come from peers behind a relay
	if !isRelayAddress(str.Conn().RemoteMultiaddr()) {
		return 0, nil, fmt.Errorf("received hole punch stream: %s", str.Conn().RemoteMultiaddr())
	}
	ownAddrs := removeRelayAddrs(s.ids.OwnObservedAddrs())
	// If we can't tell the peer where to dial us, there's no point in starting the hole punching.
	if len(ownAddrs) == 0 {
		return 0, nil, errors.New("rejecting hole punch request, as we don't have any public addresses")
	}

	if err := str.Scope().ReserveMemory(maxMsgSize, network.ReservationPriorityAlways); err != nil {
		log.Debugf("error reserving memory for stream: %s, err")
		return 0, nil, err
	}
	defer str.Scope().ReleaseMemory(maxMsgSize)

	wr := protoio.NewDelimitedWriter(str)
	rd := protoio.NewDelimitedReader(str, maxMsgSize)

	// Read Connect message
	msg := new(pb.HolePunch)

	str.SetDeadline(time.Now().Add(StreamTimeout))

	if err := rd.ReadMsg(msg); err != nil {
		return 0, nil, fmt.Errorf("failed to read message from initator: %w", err)
	}
	if t := msg.GetType(); t != pb.HolePunch_CONNECT {
		return 0, nil, fmt.Errorf("expected CONNECT message from initiator but got %d", t)
	}
	obsDial := removeRelayAddrs(addrsFromBytes(msg.ObsAddrs))
	log.Debugw("received hole punch request", "peer", str.Conn().RemotePeer(), "addrs", obsDial)
	if len(obsDial) == 0 {
		return 0, nil, errors.New("expected CONNECT message to contain at least one address")
	}

	// Write CONNECT message
	msg.Reset()
	msg.Type = pb.HolePunch_CONNECT.Enum()
	msg.ObsAddrs = addrsToBytes(ownAddrs)
	tstart := time.Now()
	if err := wr.WriteMsg(msg); err != nil {
		return 0, nil, fmt.Errorf("failed to write CONNECT message to initator: %w", err)
	}

	// Read SYNC message
	msg.Reset()
	if err := rd.ReadMsg(msg); err != nil {
		return 0, nil, fmt.Errorf("failed to read message from initator: %w", err)
	}
	if t := msg.GetType(); t != pb.HolePunch_SYNC {
		return 0, nil, fmt.Errorf("expected SYNC message from initiator but got %d", t)
	}
	return time.Since(tstart), obsDial, nil
}

func (s *Service) handleNewStream(str network.Stream) {
	// Check directionality of the underlying connection.
	// Peer A receives an inbound connection from peer B.
	// Peer A opens a new hole punch stream to peer B.
	// Peer B receives this stream, calling this function.
	// Peer B sees the underlying connection as an outbound connection.
	if str.Conn().Stat().Direction == network.DirInbound {
		str.Reset()
		return
	}

	if err := str.Scope().SetService(ServiceName); err != nil {
		log.Debugf("error attaching stream to holepunch service: %s", err)
		str.Reset()
		return
	}

	rp := str.Conn().RemotePeer()
	rtt, addrs, err := s.incomingHolePunch(str)
	if err != nil {
		s.tracer.ProtocolError(rp, err)
		log.Debugw("error handling holepunching stream from", "peer", rp, "error", err)
		str.Reset()
		return
	}
	str.Close()

	// Hole punch now by forcing a connect
	pi := peer.AddrInfo{
		ID:    rp,
		Addrs: addrs,
	}
	s.tracer.StartHolePunch(rp, addrs, rtt)
	log.Debugw("starting hole punch", "peer", rp)
	start := time.Now()
	s.tracer.HolePunchAttempt(pi.ID)
	err = holePunchConnect(s.ctx, s.host, pi, false)
	dt := time.Since(start)
	s.tracer.EndHolePunch(rp, dt, err)
}

// DirectConnect is only exposed for testing purposes.
// TODO: find a solution for this.
func (s *Service) DirectConnect(p peer.ID) error {
	<-s.hasPublicAddrsChan
	s.holePuncherMx.Lock()
	holePuncher := s.holePuncher
	s.holePuncherMx.Unlock()
	return holePuncher.DirectConnect(p)
}
