package relay

import (
	"context"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	pb "github.com/libp2p/go-libp2p/p2p/protocol/circuitv1/pb"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/util"

	logging "github.com/ipfs/go-log/v2"
	pool "github.com/libp2p/go-buffer-pool"
	ma "github.com/multiformats/go-multiaddr"
)

var log = logging.Logger("relay")

const (
	ProtoID = "/libp2p/circuit/relay/0.1.0"

	ServiceName = "libp2p.relay/v1"

	StreamTimeout    = time.Minute
	ConnectTimeout   = 30 * time.Second
	HandshakeTimeout = time.Minute

	relayHopTag      = "relay-v1-hop"
	relayHopTagValue = 2

	maxMessageSize = 4096
)

type Relay struct {
	closed int32
	ctx    context.Context
	cancel context.CancelFunc

	host  host.Host
	rc    Resources
	acl   ACLFilter
	scope network.ResourceScopeSpan

	mx     sync.Mutex
	conns  map[peer.ID]int
	active int
}

func NewRelay(h host.Host, opts ...Option) (*Relay, error) {
	r := &Relay{
		host:  h,
		rc:    DefaultResources(),
		conns: make(map[peer.ID]int),
	}
	r.ctx, r.cancel = context.WithCancel(context.Background())

	for _, opt := range opts {
		err := opt(r)
		if err != nil {
			return nil, fmt.Errorf("error applying relay option: %w", err)
		}
	}

	// get a scope for memory reservations at service level
	err := h.Network().ResourceManager().ViewService(ServiceName,
		func(s network.ServiceScope) error {
			var err error
			r.scope, err = s.BeginSpan()
			return err
		})
	if err != nil {
		return nil, err
	}

	h.SetStreamHandler(ProtoID, r.handleStream)

	return r, nil
}

func (r *Relay) Close() error {
	if atomic.CompareAndSwapInt32(&r.closed, 0, 1) {
		r.host.RemoveStreamHandler(ProtoID)
		r.scope.Done()
		r.cancel()
	}
	return nil
}

func (r *Relay) handleStream(s network.Stream) {
	log.Debugf("new relay stream from: %s", s.Conn().RemotePeer())

	if err := s.Scope().SetService(ServiceName); err != nil {
		log.Debugf("error attaching stream to relay service: %s", err)
		s.Reset()
		return
	}

	if err := s.Scope().ReserveMemory(maxMessageSize, network.ReservationPriorityAlways); err != nil {
		log.Debugf("error reserving memory for stream: %s", err)
		s.Reset()
		return
	}
	defer s.Scope().ReleaseMemory(maxMessageSize)

	rd := util.NewDelimitedReader(s, maxMessageSize)
	defer rd.Close()

	s.SetReadDeadline(time.Now().Add(StreamTimeout))

	var msg pb.CircuitRelay

	err := rd.ReadMsg(&msg)
	if err != nil {
		r.handleError(s, pb.CircuitRelay_MALFORMED_MESSAGE)
		return
	}
	s.SetReadDeadline(time.Time{})

	switch msg.GetType() {
	case pb.CircuitRelay_HOP:
		r.handleHopStream(s, &msg)
	case pb.CircuitRelay_CAN_HOP:
		r.handleCanHop(s, &msg)
	case pb.CircuitRelay_STOP:
		r.handleError(s, pb.CircuitRelay_STOP_RELAY_REFUSED)
	default:
		log.Warnf("unexpected relay handshake: %d", msg.GetType())
		r.handleError(s, pb.CircuitRelay_MALFORMED_MESSAGE)
	}
}

func (r *Relay) handleHopStream(s network.Stream, msg *pb.CircuitRelay) {
	span, err := r.scope.BeginSpan()
	if err != nil {
		log.Debugf("failed to begin relay transaction: %s", err)
		r.handleError(s, pb.CircuitRelay_HOP_CANT_SPEAK_RELAY)
		return
	}

	fail := func(code pb.CircuitRelay_Status) {
		span.Done()
		r.handleError(s, code)
	}

	// reserve buffers for the relay
	if err := span.ReserveMemory(2*r.rc.BufferSize, network.ReservationPriorityHigh); err != nil {
		log.Debugf("error reserving memory for relay: %s", err)
		fail(pb.CircuitRelay_HOP_CANT_SPEAK_RELAY)
		return
	}

	src, err := peerToPeerInfo(msg.GetSrcPeer())
	if err != nil {
		fail(pb.CircuitRelay_HOP_SRC_MULTIADDR_INVALID)
		return
	}

	if src.ID != s.Conn().RemotePeer() {
		fail(pb.CircuitRelay_HOP_SRC_MULTIADDR_INVALID)
		return
	}

	dest, err := peerToPeerInfo(msg.GetDstPeer())
	if err != nil {
		fail(pb.CircuitRelay_HOP_DST_MULTIADDR_INVALID)
		return
	}

	if dest.ID == r.host.ID() {
		fail(pb.CircuitRelay_HOP_CANT_RELAY_TO_SELF)
		return
	}

	if r.acl != nil && !r.acl.AllowHop(src.ID, dest.ID) {
		log.Debugf("refusing hop from %s to %s; ACL refused", src.ID, dest.ID)
		fail(pb.CircuitRelay_HOP_CANT_SPEAK_RELAY)
		return
	}

	r.mx.Lock()
	if r.active >= r.rc.MaxCircuits {
		r.mx.Unlock()
		log.Debugf("refusing connection from %s to %s; too many active circuits", src.ID, dest.ID)
		fail(pb.CircuitRelay_HOP_CANT_SPEAK_RELAY)
		return
	}

	srcConns := r.conns[src.ID]
	if srcConns >= r.rc.MaxCircuitsPerPeer {
		r.mx.Unlock()
		log.Debugf("refusing connection from %s to %s; too many connections from %s", src.ID, dest.ID, src)
		fail(pb.CircuitRelay_HOP_CANT_SPEAK_RELAY)
		return
	}

	destConns := r.conns[dest.ID]
	if destConns >= r.rc.MaxCircuitsPerPeer {
		r.mx.Unlock()
		log.Debugf("refusing connection from %s to %s; too many connecitons to %s", src.ID, dest.ID, dest.ID)
		fail(pb.CircuitRelay_HOP_CANT_SPEAK_RELAY)
		return
	}

	r.active++
	r.addConn(src.ID)
	r.addConn(src.ID)
	r.mx.Unlock()

	cleanup := func() {
		span.Done()
		r.mx.Lock()
		r.active--
		r.rmConn(src.ID)
		r.rmConn(dest.ID)
		r.mx.Unlock()
	}

	// open stream
	ctx, cancel := context.WithTimeout(r.ctx, ConnectTimeout)
	defer cancel()

	ctx = network.WithNoDial(ctx, "relay hop")
	bs, err := r.host.NewStream(ctx, dest.ID, ProtoID)
	if err != nil {
		log.Debugf("error opening relay stream to %s: %s", dest.ID.Pretty(), err.Error())
		if err == network.ErrNoConn {
			r.handleError(s, pb.CircuitRelay_HOP_NO_CONN_TO_DST)
		} else {
			r.handleError(s, pb.CircuitRelay_HOP_CANT_DIAL_DST)
		}
		cleanup()
		return
	}

	fail = func(code pb.CircuitRelay_Status) {
		bs.Reset()
		cleanup()
		r.handleError(s, code)
	}

	if err := bs.Scope().SetService(ServiceName); err != nil {
		log.Debugf("error attaching stream to relay service: %s", err)
		fail(pb.CircuitRelay_HOP_CANT_SPEAK_RELAY)
		return
	}

	// stop handshake
	if err := bs.Scope().ReserveMemory(maxMessageSize, network.ReservationPriorityAlways); err != nil {
		log.Debugf("failed to reserve memory for stream: %s", err)
		fail(pb.CircuitRelay_HOP_CANT_SPEAK_RELAY)
		return
	}
	defer bs.Scope().ReleaseMemory(maxMessageSize)

	rd := util.NewDelimitedReader(bs, maxMessageSize)
	wr := util.NewDelimitedWriter(bs)
	defer rd.Close()

	// set handshake deadline
	bs.SetDeadline(time.Now().Add(HandshakeTimeout))

	msg.Type = pb.CircuitRelay_STOP.Enum()

	err = wr.WriteMsg(msg)
	if err != nil {
		log.Debugf("error writing stop handshake: %s", err.Error())
		fail(pb.CircuitRelay_HOP_CANT_OPEN_DST_STREAM)
		return
	}

	msg.Reset()

	err = rd.ReadMsg(msg)
	if err != nil {
		log.Debugf("error reading stop response: %s", err.Error())
		fail(pb.CircuitRelay_HOP_CANT_OPEN_DST_STREAM)
		return
	}

	if msg.GetType() != pb.CircuitRelay_STATUS {
		log.Debugf("unexpected relay stop response: not a status message (%d)", msg.GetType())
		fail(pb.CircuitRelay_HOP_CANT_OPEN_DST_STREAM)
		return
	}

	if msg.GetCode() != pb.CircuitRelay_SUCCESS {
		log.Debugf("relay stop failure: %d", msg.GetCode())
		fail(msg.GetCode())
		return
	}

	err = r.writeResponse(s, pb.CircuitRelay_SUCCESS)
	if err != nil {
		log.Debugf("error writing relay response: %s", err.Error())
		bs.Reset()
		s.Reset()
		cleanup()
		return
	}

	// relay connection
	log.Infof("relaying connection between %s and %s", src.ID.Pretty(), dest.ID.Pretty())

	// reset deadline
	bs.SetDeadline(time.Time{})

	goroutines := new(int32)
	*goroutines = 2
	done := func() {
		if atomic.AddInt32(goroutines, -1) == 0 {
			s.Close()
			bs.Close()
			cleanup()
		}
	}

	go r.relayConn(s, bs, src.ID, dest.ID, done)
	go r.relayConn(bs, s, dest.ID, src.ID, done)
}

func (r *Relay) addConn(p peer.ID) {
	conns := r.conns[p]
	conns++
	r.conns[p] = conns
	if conns == 1 {
		r.host.ConnManager().TagPeer(p, relayHopTag, relayHopTagValue)
	}
}

func (r *Relay) rmConn(p peer.ID) {
	conns := r.conns[p]
	conns--
	if conns > 0 {
		r.conns[p] = conns
	} else {
		delete(r.conns, p)
		r.host.ConnManager().UntagPeer(p, relayHopTag)
	}
}

func (r *Relay) relayConn(src, dest network.Stream, srcID, destID peer.ID, done func()) {
	defer done()

	buf := pool.Get(r.rc.BufferSize)
	defer pool.Put(buf)

	count, err := io.CopyBuffer(dest, src, buf)
	if err != nil {
		log.Debugf("relay copy error: %s", err)
		// Reset both.
		src.Reset()
		dest.Reset()
	} else {
		// propagate the close
		dest.CloseWrite()
	}

	log.Debugf("relayed %d bytes from %s to %s", count, srcID, destID)
}

func (r *Relay) handleCanHop(s network.Stream, msg *pb.CircuitRelay) {
	err := r.writeResponse(s, pb.CircuitRelay_SUCCESS)

	if err != nil {
		s.Reset()
		log.Debugf("error writing relay response: %s", err.Error())
	} else {
		s.Close()
	}
}

func (r *Relay) handleError(s network.Stream, code pb.CircuitRelay_Status) {
	log.Warnf("relay error: %s", code)
	err := r.writeResponse(s, code)
	if err != nil {
		s.Reset()
		log.Debugf("error writing relay response: %s", err.Error())
	} else {
		s.Close()
	}
}

// Queries a peer for support of hop relay
func CanHop(ctx context.Context, host host.Host, id peer.ID) (bool, error) {
	s, err := host.NewStream(ctx, id, ProtoID)
	if err != nil {
		return false, err
	}
	defer s.Close()

	rd := util.NewDelimitedReader(s, maxMessageSize)
	wr := util.NewDelimitedWriter(s)
	defer rd.Close()

	var msg pb.CircuitRelay

	msg.Type = pb.CircuitRelay_CAN_HOP.Enum()

	if err := wr.WriteMsg(&msg); err != nil {
		s.Reset()
		return false, err
	}

	msg.Reset()

	if err := rd.ReadMsg(&msg); err != nil {
		s.Reset()
		return false, err
	}

	if msg.GetType() != pb.CircuitRelay_STATUS {
		return false, fmt.Errorf("unexpected relay response; not a status message (%d)", msg.GetType())
	}

	return msg.GetCode() == pb.CircuitRelay_SUCCESS, nil
}

func (r *Relay) writeResponse(s network.Stream, code pb.CircuitRelay_Status) error {
	wr := util.NewDelimitedWriter(s)

	var msg pb.CircuitRelay
	msg.Type = pb.CircuitRelay_STATUS.Enum()
	msg.Code = code.Enum()

	return wr.WriteMsg(&msg)
}

func peerToPeerInfo(p *pb.CircuitRelay_Peer) (peer.AddrInfo, error) {
	if p == nil {
		return peer.AddrInfo{}, fmt.Errorf("nil peer")
	}

	id, err := peer.IDFromBytes(p.Id)
	if err != nil {
		return peer.AddrInfo{}, err
	}

	addrs := make([]ma.Multiaddr, 0, len(p.Addrs))
	for _, addrBytes := range p.Addrs {
		a, err := ma.NewMultiaddrBytes(addrBytes)
		if err == nil {
			addrs = append(addrs, a)
		}
	}

	return peer.AddrInfo{ID: id, Addrs: addrs}, nil
}
