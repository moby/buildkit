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
	"github.com/libp2p/go-libp2p/core/record"
	pbv2 "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/pb"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/proto"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/util"

	logging "github.com/ipfs/go-log/v2"
	pool "github.com/libp2p/go-buffer-pool"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

const (
	ServiceName = "libp2p.relay/v2"

	ReservationTagWeight = 10

	StreamTimeout    = time.Minute
	ConnectTimeout   = 30 * time.Second
	HandshakeTimeout = time.Minute

	relayHopTag      = "relay-v2-hop"
	relayHopTagValue = 2

	maxMessageSize = 4096
)

var log = logging.Logger("relay")

// Relay is the (limited) relay service object.
type Relay struct {
	closed uint32
	ctx    context.Context
	cancel func()

	host        host.Host
	rc          Resources
	acl         ACLFilter
	constraints *constraints
	scope       network.ResourceScopeSpan

	mx    sync.Mutex
	rsvp  map[peer.ID]time.Time
	conns map[peer.ID]int

	selfAddr ma.Multiaddr
}

// New constructs a new limited relay that can provide relay services in the given host.
func New(h host.Host, opts ...Option) (*Relay, error) {
	ctx, cancel := context.WithCancel(context.Background())

	r := &Relay{
		ctx:    ctx,
		cancel: cancel,
		host:   h,
		rc:     DefaultResources(),
		acl:    nil,
		rsvp:   make(map[peer.ID]time.Time),
		conns:  make(map[peer.ID]int),
	}

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

	r.constraints = newConstraints(&r.rc)
	r.selfAddr = ma.StringCast(fmt.Sprintf("/p2p/%s", h.ID()))

	h.SetStreamHandler(proto.ProtoIDv2Hop, r.handleStream)
	h.Network().Notify(
		&network.NotifyBundle{
			DisconnectedF: r.disconnected,
		})
	go r.background()

	return r, nil
}

func (r *Relay) Close() error {
	if atomic.CompareAndSwapUint32(&r.closed, 0, 1) {
		r.host.RemoveStreamHandler(proto.ProtoIDv2Hop)
		r.scope.Done()
		r.cancel()
		r.mx.Lock()
		for p := range r.rsvp {
			r.host.ConnManager().UntagPeer(p, "relay-reservation")
		}
		r.mx.Unlock()
	}
	return nil
}

func (r *Relay) handleStream(s network.Stream) {
	log.Infof("new relay stream from: %s", s.Conn().RemotePeer())

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

	var msg pbv2.HopMessage

	err := rd.ReadMsg(&msg)
	if err != nil {
		r.handleError(s, pbv2.Status_MALFORMED_MESSAGE)
		return
	}
	// reset stream deadline as message has been read
	s.SetReadDeadline(time.Time{})

	switch msg.GetType() {
	case pbv2.HopMessage_RESERVE:
		r.handleReserve(s)

	case pbv2.HopMessage_CONNECT:
		r.handleConnect(s, &msg)

	default:
		r.handleError(s, pbv2.Status_MALFORMED_MESSAGE)
	}
}

func (r *Relay) handleReserve(s network.Stream) {
	defer s.Close()

	p := s.Conn().RemotePeer()
	a := s.Conn().RemoteMultiaddr()

	if isRelayAddr(a) {
		log.Debugf("refusing relay reservation for %s; reservation attempt over relay connection")
		r.handleError(s, pbv2.Status_PERMISSION_DENIED)
		return
	}

	if r.acl != nil && !r.acl.AllowReserve(p, a) {
		log.Debugf("refusing relay reservation for %s; permission denied", p)
		r.handleError(s, pbv2.Status_PERMISSION_DENIED)
		return
	}

	r.mx.Lock()
	now := time.Now()

	_, exists := r.rsvp[p]
	if !exists {
		if err := r.constraints.AddReservation(p, a); err != nil {
			r.mx.Unlock()
			log.Debugf("refusing relay reservation for %s; IP constraint violation: %s", p, err)
			r.handleError(s, pbv2.Status_RESERVATION_REFUSED)
			return
		}
	}

	expire := now.Add(r.rc.ReservationTTL)
	r.rsvp[p] = expire
	r.host.ConnManager().TagPeer(p, "relay-reservation", ReservationTagWeight)
	r.mx.Unlock()

	log.Debugf("reserving relay slot for %s", p)

	// Delivery of the reservation might fail for a number of reasons.
	// For example, the stream might be reset or the connection might be closed before the reservation is received.
	// In that case, the reservation will just be garbage collected later.
	if err := r.writeResponse(s, pbv2.Status_OK, r.makeReservationMsg(p, expire), r.makeLimitMsg(p)); err != nil {
		log.Debugf("error writing reservation response; retracting reservation for %s", p)
		s.Reset()
	}
}

func (r *Relay) handleConnect(s network.Stream, msg *pbv2.HopMessage) {
	src := s.Conn().RemotePeer()
	a := s.Conn().RemoteMultiaddr()

	span, err := r.scope.BeginSpan()
	if err != nil {
		log.Debugf("failed to begin relay transaction: %s", err)
		r.handleError(s, pbv2.Status_RESOURCE_LIMIT_EXCEEDED)
		return
	}

	fail := func(status pbv2.Status) {
		span.Done()
		r.handleError(s, status)
	}

	// reserve buffers for the relay
	if err := span.ReserveMemory(2*r.rc.BufferSize, network.ReservationPriorityHigh); err != nil {
		log.Debugf("error reserving memory for relay: %s", err)
		fail(pbv2.Status_RESOURCE_LIMIT_EXCEEDED)
		return
	}

	if isRelayAddr(a) {
		log.Debugf("refusing connection from %s; connection attempt over relay connection")
		fail(pbv2.Status_PERMISSION_DENIED)
		return
	}

	dest, err := util.PeerToPeerInfoV2(msg.GetPeer())
	if err != nil {
		fail(pbv2.Status_MALFORMED_MESSAGE)
		return
	}

	if r.acl != nil && !r.acl.AllowConnect(src, s.Conn().RemoteMultiaddr(), dest.ID) {
		log.Debugf("refusing connection from %s to %s; permission denied", src, dest.ID)
		fail(pbv2.Status_PERMISSION_DENIED)
		return
	}

	r.mx.Lock()
	_, rsvp := r.rsvp[dest.ID]
	if !rsvp {
		r.mx.Unlock()
		log.Debugf("refusing connection from %s to %s; no reservation", src, dest.ID)
		fail(pbv2.Status_NO_RESERVATION)
		return
	}

	srcConns := r.conns[src]
	if srcConns >= r.rc.MaxCircuits {
		r.mx.Unlock()
		log.Debugf("refusing connection from %s to %s; too many connections from %s", src, dest.ID, src)
		fail(pbv2.Status_RESOURCE_LIMIT_EXCEEDED)
		return
	}

	destConns := r.conns[dest.ID]
	if destConns >= r.rc.MaxCircuits {
		r.mx.Unlock()
		log.Debugf("refusing connection from %s to %s; too many connecitons to %s", src, dest.ID, dest.ID)
		fail(pbv2.Status_RESOURCE_LIMIT_EXCEEDED)
		return
	}

	r.addConn(src)
	r.addConn(dest.ID)
	r.mx.Unlock()

	cleanup := func() {
		span.Done()
		r.mx.Lock()
		r.rmConn(src)
		r.rmConn(dest.ID)
		r.mx.Unlock()
	}

	ctx, cancel := context.WithTimeout(r.ctx, ConnectTimeout)
	defer cancel()

	ctx = network.WithNoDial(ctx, "relay connect")

	bs, err := r.host.NewStream(ctx, dest.ID, proto.ProtoIDv2Stop)
	if err != nil {
		log.Debugf("error opening relay stream to %s: %s", dest.ID, err)
		cleanup()
		r.handleError(s, pbv2.Status_CONNECTION_FAILED)
		return
	}

	fail = func(status pbv2.Status) {
		bs.Reset()
		cleanup()
		r.handleError(s, status)
	}

	if err := bs.Scope().SetService(ServiceName); err != nil {
		log.Debugf("error attaching stream to relay service: %s", err)
		fail(pbv2.Status_RESOURCE_LIMIT_EXCEEDED)
		return
	}

	// handshake
	if err := bs.Scope().ReserveMemory(maxMessageSize, network.ReservationPriorityAlways); err != nil {
		log.Debugf("erro reserving memory for stream: %s", err)
		fail(pbv2.Status_RESOURCE_LIMIT_EXCEEDED)
		return
	}
	defer bs.Scope().ReleaseMemory(maxMessageSize)

	rd := util.NewDelimitedReader(bs, maxMessageSize)
	wr := util.NewDelimitedWriter(bs)
	defer rd.Close()

	var stopmsg pbv2.StopMessage
	stopmsg.Type = pbv2.StopMessage_CONNECT.Enum()
	stopmsg.Peer = util.PeerInfoToPeerV2(peer.AddrInfo{ID: src})
	stopmsg.Limit = r.makeLimitMsg(dest.ID)

	bs.SetDeadline(time.Now().Add(HandshakeTimeout))

	err = wr.WriteMsg(&stopmsg)
	if err != nil {
		log.Debugf("error writing stop handshake")
		fail(pbv2.Status_CONNECTION_FAILED)
		return
	}

	stopmsg.Reset()

	err = rd.ReadMsg(&stopmsg)
	if err != nil {
		log.Debugf("error reading stop response: %s", err.Error())
		fail(pbv2.Status_CONNECTION_FAILED)
		return
	}

	if t := stopmsg.GetType(); t != pbv2.StopMessage_STATUS {
		log.Debugf("unexpected stop response; not a status message (%d)", t)
		fail(pbv2.Status_CONNECTION_FAILED)
		return
	}

	if status := stopmsg.GetStatus(); status != pbv2.Status_OK {
		log.Debugf("relay stop failure: %d", status)
		fail(pbv2.Status_CONNECTION_FAILED)
		return
	}

	var response pbv2.HopMessage
	response.Type = pbv2.HopMessage_STATUS.Enum()
	response.Status = pbv2.Status_OK.Enum()
	response.Limit = r.makeLimitMsg(dest.ID)

	wr = util.NewDelimitedWriter(s)
	err = wr.WriteMsg(&response)
	if err != nil {
		log.Debugf("error writing relay response: %s", err)
		bs.Reset()
		s.Reset()
		cleanup()
		return
	}

	// reset deadline
	bs.SetDeadline(time.Time{})

	log.Infof("relaying connection from %s to %s", src, dest.ID)

	goroutines := new(int32)
	*goroutines = 2

	done := func() {
		if atomic.AddInt32(goroutines, -1) == 0 {
			s.Close()
			bs.Close()
			cleanup()
		}
	}

	if r.rc.Limit != nil {
		deadline := time.Now().Add(r.rc.Limit.Duration)
		s.SetDeadline(deadline)
		bs.SetDeadline(deadline)
		go r.relayLimited(s, bs, src, dest.ID, r.rc.Limit.Data, done)
		go r.relayLimited(bs, s, dest.ID, src, r.rc.Limit.Data, done)
	} else {
		go r.relayUnlimited(s, bs, src, dest.ID, done)
		go r.relayUnlimited(bs, s, dest.ID, src, done)
	}
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

func (r *Relay) relayLimited(src, dest network.Stream, srcID, destID peer.ID, limit int64, done func()) {
	defer done()

	buf := pool.Get(r.rc.BufferSize)
	defer pool.Put(buf)

	limitedSrc := io.LimitReader(src, limit)

	count, err := io.CopyBuffer(dest, limitedSrc, buf)
	if err != nil {
		log.Debugf("relay copy error: %s", err)
		// Reset both.
		src.Reset()
		dest.Reset()
	} else {
		// propagate the close
		dest.CloseWrite()
		if count == limit {
			// we've reached the limit, discard further input
			src.CloseRead()
		}
	}

	log.Debugf("relayed %d bytes from %s to %s", count, srcID, destID)
}

func (r *Relay) relayUnlimited(src, dest network.Stream, srcID, destID peer.ID, done func()) {
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

func (r *Relay) handleError(s network.Stream, status pbv2.Status) {
	log.Debugf("relay error: %s (%d)", pbv2.Status_name[int32(status)], status)
	err := r.writeResponse(s, status, nil, nil)
	if err != nil {
		s.Reset()
		log.Debugf("error writing relay response: %s", err.Error())
	} else {
		s.Close()
	}
}

func (r *Relay) writeResponse(s network.Stream, status pbv2.Status, rsvp *pbv2.Reservation, limit *pbv2.Limit) error {
	wr := util.NewDelimitedWriter(s)

	var msg pbv2.HopMessage
	msg.Type = pbv2.HopMessage_STATUS.Enum()
	msg.Status = status.Enum()
	msg.Reservation = rsvp
	msg.Limit = limit

	return wr.WriteMsg(&msg)
}

func (r *Relay) makeReservationMsg(p peer.ID, expire time.Time) *pbv2.Reservation {
	expireUnix := uint64(expire.Unix())

	var addrBytes [][]byte
	for _, addr := range r.host.Addrs() {
		if !manet.IsPublicAddr(addr) {
			continue
		}

		addr = addr.Encapsulate(r.selfAddr)
		addrBytes = append(addrBytes, addr.Bytes())
	}

	rsvp := &pbv2.Reservation{
		Expire: &expireUnix,
		Addrs:  addrBytes,
	}

	voucher := &proto.ReservationVoucher{
		Relay:      r.host.ID(),
		Peer:       p,
		Expiration: expire,
	}

	envelope, err := record.Seal(voucher, r.host.Peerstore().PrivKey(r.host.ID()))
	if err != nil {
		log.Errorf("error sealing voucher for %s: %s", p, err)
		return rsvp
	}

	blob, err := envelope.Marshal()
	if err != nil {
		log.Errorf("error marshalling voucher for %s: %s", p, err)
		return rsvp
	}

	rsvp.Voucher = blob

	return rsvp
}

func (r *Relay) makeLimitMsg(p peer.ID) *pbv2.Limit {
	if r.rc.Limit == nil {
		return nil
	}

	duration := uint32(r.rc.Limit.Duration / time.Second)
	data := uint64(r.rc.Limit.Data)

	return &pbv2.Limit{
		Duration: &duration,
		Data:     &data,
	}
}

func (r *Relay) background() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			r.gc()
		case <-r.ctx.Done():
			return
		}
	}
}

func (r *Relay) gc() {
	r.mx.Lock()
	defer r.mx.Unlock()

	now := time.Now()

	for p, expire := range r.rsvp {
		if expire.Before(now) {
			delete(r.rsvp, p)
			r.host.ConnManager().UntagPeer(p, "relay-reservation")
		}
	}

	for p, count := range r.conns {
		if count == 0 {
			delete(r.conns, p)
		}
	}
}

func (r *Relay) disconnected(n network.Network, c network.Conn) {
	p := c.RemotePeer()
	if n.Connectedness(p) == network.Connected {
		return
	}

	r.mx.Lock()
	defer r.mx.Unlock()

	delete(r.rsvp, p)
}

func isRelayAddr(a ma.Multiaddr) bool {
	_, err := a.ValueForProtocol(ma.P_CIRCUIT)
	return err == nil
}
