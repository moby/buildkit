package identify

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/core/record"

	"github.com/libp2p/go-libp2p/p2p/host/eventbus"
	pb "github.com/libp2p/go-libp2p/p2p/protocol/identify/pb"

	"github.com/libp2p/go-msgio/protoio"

	"github.com/gogo/protobuf/proto"
	logging "github.com/ipfs/go-log/v2"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
	msmux "github.com/multiformats/go-multistream"
)

var log = logging.Logger("net/identify")

// ID is the protocol.ID of version 1.0.0 of the identify
// service.
const ID = "/ipfs/id/1.0.0"

// LibP2PVersion holds the current protocol version for a client running this code
// TODO(jbenet): fix the versioning mess.
// XXX: Don't change this till 2020. You'll break all go-ipfs versions prior to
// 0.4.17 which asserted an exact version match.
const LibP2PVersion = "ipfs/0.1.0"

const ServiceName = "libp2p.identify"

const maxPushConcurrency = 32

// StreamReadTimeout is the read timeout on all incoming Identify family streams.
var StreamReadTimeout = 60 * time.Second

var (
	legacyIDSize     = 2 * 1024 // 2k Bytes
	signedIDSize     = 8 * 1024 // 8K
	maxMessages      = 10
	defaultUserAgent = "github.com/libp2p/go-libp2p"
)

type addPeerHandlerReq struct {
	rp   peer.ID
	resp chan *peerHandler
}

type rmPeerHandlerReq struct {
	p peer.ID
}

type IDService interface {
	// IdentifyConn synchronously triggers an identify request on the connection and
	// waits for it to complete. If the connection is being identified by another
	// caller, this call will wait. If the connection has already been identified,
	// it will return immediately.
	IdentifyConn(network.Conn)
	// IdentifyWait triggers an identify (if the connection has not already been
	// identified) and returns a channel that is closed when the identify protocol
	// completes.
	IdentifyWait(network.Conn) <-chan struct{}
	// OwnObservedAddrs returns the addresses peers have reported we've dialed from
	OwnObservedAddrs() []ma.Multiaddr
	// ObservedAddrsFor returns the addresses peers have reported we've dialed from,
	// for a specific local address.
	ObservedAddrsFor(local ma.Multiaddr) []ma.Multiaddr
	io.Closer
}

// idService is a structure that implements ProtocolIdentify.
// It is a trivial service that gives the other peer some
// useful information about the local peer. A sort of hello.
//
// The idService sends:
//   - Our IPFS Protocol Version
//   - Our IPFS Agent Version
//   - Our public Listen Addresses
type idService struct {
	Host      host.Host
	UserAgent string

	ctx       context.Context
	ctxCancel context.CancelFunc
	// track resources that need to be shut down before we shut down
	refCount sync.WaitGroup

	disableSignedPeerRecord bool

	// Identified connections (finished and in progress).
	connsMu sync.RWMutex
	conns   map[network.Conn]chan struct{}

	addrMu sync.Mutex

	// our own observed addresses.
	observedAddrs *ObservedAddrManager

	emitters struct {
		evtPeerProtocolsUpdated        event.Emitter
		evtPeerIdentificationCompleted event.Emitter
		evtPeerIdentificationFailed    event.Emitter
	}

	addPeerHandlerCh chan addPeerHandlerReq
	rmPeerHandlerCh  chan rmPeerHandlerReq

	// pushSemaphore limits the push/delta concurrency to avoid storms
	// that clog the transient scope.
	pushSemaphore chan struct{}
}

// NewIDService constructs a new *idService and activates it by
// attaching its stream handler to the given host.Host.
func NewIDService(h host.Host, opts ...Option) (*idService, error) {
	var cfg config
	for _, opt := range opts {
		opt(&cfg)
	}

	userAgent := defaultUserAgent
	if cfg.userAgent != "" {
		userAgent = cfg.userAgent
	}

	s := &idService{
		Host:      h,
		UserAgent: userAgent,

		conns: make(map[network.Conn]chan struct{}),

		disableSignedPeerRecord: cfg.disableSignedPeerRecord,

		addPeerHandlerCh: make(chan addPeerHandlerReq),
		rmPeerHandlerCh:  make(chan rmPeerHandlerReq),

		pushSemaphore: make(chan struct{}, maxPushConcurrency),
	}
	s.ctx, s.ctxCancel = context.WithCancel(context.Background())

	// handle local protocol handler updates, and push deltas to peers.
	var err error

	observedAddrs, err := NewObservedAddrManager(h)
	if err != nil {
		return nil, fmt.Errorf("failed to create observed address manager: %s", err)
	}
	s.observedAddrs = observedAddrs

	s.refCount.Add(1)
	go s.loop()

	s.emitters.evtPeerProtocolsUpdated, err = h.EventBus().Emitter(&event.EvtPeerProtocolsUpdated{})
	if err != nil {
		log.Warnf("identify service not emitting peer protocol updates; err: %s", err)
	}
	s.emitters.evtPeerIdentificationCompleted, err = h.EventBus().Emitter(&event.EvtPeerIdentificationCompleted{})
	if err != nil {
		log.Warnf("identify service not emitting identification completed events; err: %s", err)
	}
	s.emitters.evtPeerIdentificationFailed, err = h.EventBus().Emitter(&event.EvtPeerIdentificationFailed{})
	if err != nil {
		log.Warnf("identify service not emitting identification failed events; err: %s", err)
	}

	// register protocols that do not depend on peer records.
	h.SetStreamHandler(IDDelta, s.deltaHandler)
	h.SetStreamHandler(ID, s.sendIdentifyResp)
	h.SetStreamHandler(IDPush, s.pushHandler)

	h.Network().Notify((*netNotifiee)(s))
	return s, nil
}

func (ids *idService) loop() {
	defer ids.refCount.Done()

	phs := make(map[peer.ID]*peerHandler)
	sub, err := ids.Host.EventBus().Subscribe([]interface{}{&event.EvtLocalProtocolsUpdated{},
		&event.EvtLocalAddressesUpdated{}}, eventbus.BufSize(256))
	if err != nil {
		log.Errorf("failed to subscribe to events on the bus, err=%s", err)
		return
	}

	phClosedCh := make(chan peer.ID)

	defer func() {
		sub.Close()
		// The context will cancel the workers. Now, wait for them to
		// exit.
		for range phs {
			<-phClosedCh
		}
	}()

	// Use a fresh context for the handlers. Otherwise, they'll get canceled
	// before we're ready to shutdown and they'll have "stopped" without us
	// _calling_ stop.
	handlerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	for {
		select {
		case addReq := <-ids.addPeerHandlerCh:
			rp := addReq.rp
			ph, ok := phs[rp]
			if !ok && ids.Host.Network().Connectedness(rp) == network.Connected {
				ph = newPeerHandler(rp, ids)
				ph.start(handlerCtx, func() { phClosedCh <- rp })
				phs[rp] = ph
			}
			addReq.resp <- ph
		case rmReq := <-ids.rmPeerHandlerCh:
			rp := rmReq.p
			if ids.Host.Network().Connectedness(rp) != network.Connected {
				// before we remove the peerhandler, we should ensure that it will not send any
				// more messages. Otherwise, we might create a new handler and the Identify response
				// synchronized with the new handler might be overwritten by a message sent by this "old" handler.
				ph, ok := phs[rp]
				if !ok {
					// move on, move on, there's nothing to see here.
					continue
				}
				// This is idempotent if already stopped.
				ph.stop()
			}

		case rp := <-phClosedCh:
			ph := phs[rp]

			// If we are connected to the peer, it means that we got a connection from the peer
			// before we could finish removing it's handler on the previous disconnection.
			// If we delete the handler, we wont be able to push updates to it
			// till we see a new connection. So, we should restart the handler.
			// The fact that we got the handler on this channel means that it's context and handler
			// have completed because we write the handler to this chanel only after it closed.
			if ids.Host.Network().Connectedness(rp) == network.Connected {
				ph.start(handlerCtx, func() { phClosedCh <- rp })
			} else {
				delete(phs, rp)
			}

		case e, more := <-sub.Out():
			if !more {
				return
			}
			switch e.(type) {
			case event.EvtLocalAddressesUpdated:
				for pid := range phs {
					select {
					case phs[pid].pushCh <- struct{}{}:
					default:
						log.Debugf("dropping addr updated message for %s as buffer full", pid.Pretty())
					}
				}

			case event.EvtLocalProtocolsUpdated:
				for pid := range phs {
					select {
					case phs[pid].deltaCh <- struct{}{}:
					default:
						log.Debugf("dropping protocol updated message for %s as buffer full", pid.Pretty())
					}
				}
			}

		case <-ids.ctx.Done():
			return
		}
	}
}

// Close shuts down the idService
func (ids *idService) Close() error {
	ids.ctxCancel()
	ids.observedAddrs.Close()
	ids.refCount.Wait()
	return nil
}

func (ids *idService) OwnObservedAddrs() []ma.Multiaddr {
	return ids.observedAddrs.Addrs()
}

func (ids *idService) ObservedAddrsFor(local ma.Multiaddr) []ma.Multiaddr {
	return ids.observedAddrs.AddrsFor(local)
}

func (ids *idService) IdentifyConn(c network.Conn) {
	<-ids.IdentifyWait(c)
}

func (ids *idService) IdentifyWait(c network.Conn) <-chan struct{} {
	ids.connsMu.RLock()
	wait, found := ids.conns[c]
	ids.connsMu.RUnlock()

	if found {
		return wait
	}

	ids.connsMu.Lock()
	defer ids.connsMu.Unlock()

	wait, found = ids.conns[c]
	if !found {
		wait = make(chan struct{})
		ids.conns[c] = wait

		// Spawn an identify. The connection may actually be closed
		// already, but that doesn't really matter. We'll fail to open a
		// stream then forget the connection.
		go func() {
			defer close(wait)
			if err := ids.identifyConn(c); err != nil {
				log.Warnf("failed to identify %s: %s", c.RemotePeer(), err)
				ids.emitters.evtPeerIdentificationFailed.Emit(event.EvtPeerIdentificationFailed{Peer: c.RemotePeer(), Reason: err})
				return
			}
			ids.emitters.evtPeerIdentificationCompleted.Emit(event.EvtPeerIdentificationCompleted{Peer: c.RemotePeer()})
		}()
	}

	return wait
}

func (ids *idService) removeConn(c network.Conn) {
	ids.connsMu.Lock()
	delete(ids.conns, c)
	ids.connsMu.Unlock()
}

func (ids *idService) identifyConn(c network.Conn) error {
	s, err := c.NewStream(network.WithUseTransient(context.TODO(), "identify"))
	if err != nil {
		log.Debugw("error opening identify stream", "error", err)

		// We usually do this on disconnect, but we may have already
		// processed the disconnect event.
		ids.removeConn(c)
		return err
	}

	if err := s.SetProtocol(ID); err != nil {
		log.Warnf("error setting identify protocol for stream: %s", err)
		s.Reset()
	}

	// ok give the response to our handler.
	if err := msmux.SelectProtoOrFail(ID, s); err != nil {
		log.Infow("failed negotiate identify protocol with peer", "peer", c.RemotePeer(), "error", err)
		s.Reset()
		return err
	}

	return ids.handleIdentifyResponse(s)
}

func (ids *idService) sendIdentifyResp(s network.Stream) {
	if err := s.Scope().SetService(ServiceName); err != nil {
		log.Warnf("error attaching stream to identify service: %s", err)
		s.Reset()
		return
	}

	defer s.Close()

	c := s.Conn()

	phCh := make(chan *peerHandler, 1)
	select {
	case ids.addPeerHandlerCh <- addPeerHandlerReq{c.RemotePeer(), phCh}:
	case <-ids.ctx.Done():
		return
	}

	var ph *peerHandler
	select {
	case ph = <-phCh:
	case <-ids.ctx.Done():
		return
	}

	if ph == nil {
		// Peer disconnected, abort.
		s.Reset()
		return
	}

	ph.snapshotMu.RLock()
	snapshot := ph.snapshot
	ph.snapshotMu.RUnlock()
	ids.writeChunkedIdentifyMsg(c, snapshot, s)
	log.Debugf("%s sent message to %s %s", ID, c.RemotePeer(), c.RemoteMultiaddr())
}

func (ids *idService) handleIdentifyResponse(s network.Stream) error {
	if err := s.Scope().SetService(ServiceName); err != nil {
		log.Warnf("error attaching stream to identify service: %s", err)
		s.Reset()
		return err
	}

	if err := s.Scope().ReserveMemory(signedIDSize, network.ReservationPriorityAlways); err != nil {
		log.Warnf("error reserving memory for identify stream: %s", err)
		s.Reset()
		return err
	}
	defer s.Scope().ReleaseMemory(signedIDSize)

	_ = s.SetReadDeadline(time.Now().Add(StreamReadTimeout))

	c := s.Conn()

	r := protoio.NewDelimitedReader(s, signedIDSize)
	mes := &pb.Identify{}

	if err := readAllIDMessages(r, mes); err != nil {
		log.Warn("error reading identify message: ", err)
		s.Reset()
		return err
	}

	defer s.Close()

	log.Debugf("%s received message from %s %s", s.Protocol(), c.RemotePeer(), c.RemoteMultiaddr())

	ids.consumeMessage(mes, c)

	return nil
}

func readAllIDMessages(r protoio.Reader, finalMsg proto.Message) error {
	mes := &pb.Identify{}
	for i := 0; i < maxMessages; i++ {
		switch err := r.ReadMsg(mes); err {
		case io.EOF:
			return nil
		case nil:
			proto.Merge(finalMsg, mes)
		default:
			return err
		}
	}

	return fmt.Errorf("too many parts")
}

func (ids *idService) getSnapshot() *identifySnapshot {
	snapshot := new(identifySnapshot)
	if !ids.disableSignedPeerRecord {
		if cab, ok := peerstore.GetCertifiedAddrBook(ids.Host.Peerstore()); ok {
			snapshot.record = cab.GetPeerRecord(ids.Host.ID())
		}
	}
	snapshot.addrs = ids.Host.Addrs()
	snapshot.protocols = ids.Host.Mux().Protocols()
	return snapshot
}

func (ids *idService) writeChunkedIdentifyMsg(c network.Conn, snapshot *identifySnapshot, s network.Stream) error {
	mes := ids.createBaseIdentifyResponse(c, snapshot)
	sr := ids.getSignedRecord(snapshot)
	mes.SignedPeerRecord = sr
	writer := protoio.NewDelimitedWriter(s)

	if sr == nil || proto.Size(mes) <= legacyIDSize {
		return writer.WriteMsg(mes)
	}
	mes.SignedPeerRecord = nil
	if err := writer.WriteMsg(mes); err != nil {
		return err
	}

	// then write just the signed record
	m := &pb.Identify{SignedPeerRecord: sr}
	err := writer.WriteMsg(m)
	return err

}

func (ids *idService) createBaseIdentifyResponse(
	conn network.Conn,
	snapshot *identifySnapshot,
) *pb.Identify {
	mes := &pb.Identify{}

	remoteAddr := conn.RemoteMultiaddr()
	localAddr := conn.LocalMultiaddr()

	// set protocols this node is currently handling
	mes.Protocols = snapshot.protocols

	// observed address so other side is informed of their
	// "public" address, at least in relation to us.
	mes.ObservedAddr = remoteAddr.Bytes()

	// populate unsigned addresses.
	// peers that do not yet support signed addresses will need this.
	// Note: LocalMultiaddr is sometimes 0.0.0.0
	viaLoopback := manet.IsIPLoopback(localAddr) || manet.IsIPLoopback(remoteAddr)
	mes.ListenAddrs = make([][]byte, 0, len(snapshot.addrs))
	for _, addr := range snapshot.addrs {
		if !viaLoopback && manet.IsIPLoopback(addr) {
			continue
		}
		mes.ListenAddrs = append(mes.ListenAddrs, addr.Bytes())
	}
	// set our public key
	ownKey := ids.Host.Peerstore().PubKey(ids.Host.ID())

	// check if we even have a public key.
	if ownKey == nil {
		// public key is nil. We are either using insecure transport or something erratic happened.
		// check if we're even operating in "secure mode"
		if ids.Host.Peerstore().PrivKey(ids.Host.ID()) != nil {
			// private key is present. But NO public key. Something bad happened.
			log.Errorf("did not have own public key in Peerstore")
		}
		// if neither of the key is present it is safe to assume that we are using an insecure transport.
	} else {
		// public key is present. Safe to proceed.
		if kb, err := crypto.MarshalPublicKey(ownKey); err != nil {
			log.Errorf("failed to convert key to bytes")
		} else {
			mes.PublicKey = kb
		}
	}

	// set protocol versions
	pv := LibP2PVersion
	av := ids.UserAgent
	mes.ProtocolVersion = &pv
	mes.AgentVersion = &av

	return mes
}

func (ids *idService) getSignedRecord(snapshot *identifySnapshot) []byte {
	if ids.disableSignedPeerRecord || snapshot.record == nil {
		return nil
	}

	recBytes, err := snapshot.record.Marshal()
	if err != nil {
		log.Errorw("failed to marshal signed record", "err", err)
		return nil
	}

	return recBytes
}

func (ids *idService) consumeMessage(mes *pb.Identify, c network.Conn) {
	p := c.RemotePeer()

	// mes.Protocols
	ids.Host.Peerstore().SetProtocols(p, mes.Protocols...)

	// mes.ObservedAddr
	ids.consumeObservedAddress(mes.GetObservedAddr(), c)

	// mes.ListenAddrs
	laddrs := mes.GetListenAddrs()
	lmaddrs := make([]ma.Multiaddr, 0, len(laddrs))
	for _, addr := range laddrs {
		maddr, err := ma.NewMultiaddrBytes(addr)
		if err != nil {
			log.Debugf("%s failed to parse multiaddr from %s %s", ID,
				p, c.RemoteMultiaddr())
			continue
		}
		lmaddrs = append(lmaddrs, maddr)
	}

	// NOTE: Do not add `c.RemoteMultiaddr()` to the peerstore if the remote
	// peer doesn't tell us to do so. Otherwise, we'll advertise it.
	//
	// This can cause an "addr-splosion" issue where the network will slowly
	// gossip and collect observed but unadvertised addresses. Given a NAT
	// that picks random source ports, this can cause DHT nodes to collect
	// many undialable addresses for other peers.

	// add certified addresses for the peer, if they sent us a signed peer record
	// otherwise use the unsigned addresses.
	var signedPeerRecord *record.Envelope
	signedPeerRecord, err := signedPeerRecordFromMessage(mes)
	if err != nil {
		log.Errorf("error getting peer record from Identify message: %v", err)
	}

	// Extend the TTLs on the known (probably) good addresses.
	// Taking the lock ensures that we don't concurrently process a disconnect.
	ids.addrMu.Lock()
	ttl := peerstore.RecentlyConnectedAddrTTL
	if ids.Host.Network().Connectedness(p) == network.Connected {
		ttl = peerstore.ConnectedAddrTTL
	}

	// Downgrade connected and recently connected addrs to a temporary TTL.
	for _, ttl := range []time.Duration{
		peerstore.RecentlyConnectedAddrTTL,
		peerstore.ConnectedAddrTTL,
	} {
		ids.Host.Peerstore().UpdateAddrs(p, ttl, peerstore.TempAddrTTL)
	}

	// add signed addrs if we have them and the peerstore supports them
	cab, ok := peerstore.GetCertifiedAddrBook(ids.Host.Peerstore())
	if ok && signedPeerRecord != nil {
		_, addErr := cab.ConsumePeerRecord(signedPeerRecord, ttl)
		if addErr != nil {
			log.Debugf("error adding signed addrs to peerstore: %v", addErr)
		}
	} else {
		ids.Host.Peerstore().AddAddrs(p, lmaddrs, ttl)
	}

	// Finally, expire all temporary addrs.
	ids.Host.Peerstore().UpdateAddrs(p, peerstore.TempAddrTTL, 0)
	ids.addrMu.Unlock()

	log.Debugf("%s received listen addrs for %s: %s", c.LocalPeer(), c.RemotePeer(), lmaddrs)

	// get protocol versions
	pv := mes.GetProtocolVersion()
	av := mes.GetAgentVersion()

	ids.Host.Peerstore().Put(p, "ProtocolVersion", pv)
	ids.Host.Peerstore().Put(p, "AgentVersion", av)

	// get the key from the other side. we may not have it (no-auth transport)
	ids.consumeReceivedPubKey(c, mes.PublicKey)
}

func (ids *idService) consumeReceivedPubKey(c network.Conn, kb []byte) {
	lp := c.LocalPeer()
	rp := c.RemotePeer()

	if kb == nil {
		log.Debugf("%s did not receive public key for remote peer: %s", lp, rp)
		return
	}

	newKey, err := crypto.UnmarshalPublicKey(kb)
	if err != nil {
		log.Warnf("%s cannot unmarshal key from remote peer: %s, %s", lp, rp, err)
		return
	}

	// verify key matches peer.ID
	np, err := peer.IDFromPublicKey(newKey)
	if err != nil {
		log.Debugf("%s cannot get peer.ID from key of remote peer: %s, %s", lp, rp, err)
		return
	}

	if np != rp {
		// if the newKey's peer.ID does not match known peer.ID...

		if rp == "" && np != "" {
			// if local peerid is empty, then use the new, sent key.
			err := ids.Host.Peerstore().AddPubKey(rp, newKey)
			if err != nil {
				log.Debugf("%s could not add key for %s to peerstore: %s", lp, rp, err)
			}

		} else {
			// we have a local peer.ID and it does not match the sent key... error.
			log.Errorf("%s received key for remote peer %s mismatch: %s", lp, rp, np)
		}
		return
	}

	currKey := ids.Host.Peerstore().PubKey(rp)
	if currKey == nil {
		// no key? no auth transport. set this one.
		err := ids.Host.Peerstore().AddPubKey(rp, newKey)
		if err != nil {
			log.Debugf("%s could not add key for %s to peerstore: %s", lp, rp, err)
		}
		return
	}

	// ok, we have a local key, we should verify they match.
	if currKey.Equals(newKey) {
		return // ok great. we're done.
	}

	// weird, got a different key... but the different key MATCHES the peer.ID.
	// this odd. let's log error and investigate. this should basically never happen
	// and it means we have something funky going on and possibly a bug.
	log.Errorf("%s identify got a different key for: %s", lp, rp)

	// okay... does ours NOT match the remote peer.ID?
	cp, err := peer.IDFromPublicKey(currKey)
	if err != nil {
		log.Errorf("%s cannot get peer.ID from local key of remote peer: %s, %s", lp, rp, err)
		return
	}
	if cp != rp {
		log.Errorf("%s local key for remote peer %s yields different peer.ID: %s", lp, rp, cp)
		return
	}

	// okay... curr key DOES NOT match new key. both match peer.ID. wat?
	log.Errorf("%s local key and received key for %s do not match, but match peer.ID", lp, rp)
}

// HasConsistentTransport returns true if the address 'a' shares a
// protocol set with any address in the green set. This is used
// to check if a given address might be one of the addresses a peer is
// listening on.
func HasConsistentTransport(a ma.Multiaddr, green []ma.Multiaddr) bool {
	protosMatch := func(a, b []ma.Protocol) bool {
		if len(a) != len(b) {
			return false
		}

		for i, p := range a {
			if b[i].Code != p.Code {
				return false
			}
		}
		return true
	}

	protos := a.Protocols()

	for _, ga := range green {
		if protosMatch(protos, ga.Protocols()) {
			return true
		}
	}

	return false
}

func (ids *idService) consumeObservedAddress(observed []byte, c network.Conn) {
	if observed == nil {
		return
	}

	maddr, err := ma.NewMultiaddrBytes(observed)
	if err != nil {
		log.Debugf("error parsing received observed addr for %s: %s", c, err)
		return
	}

	ids.observedAddrs.Record(c, maddr)
}

func signedPeerRecordFromMessage(msg *pb.Identify) (*record.Envelope, error) {
	if msg.SignedPeerRecord == nil || len(msg.SignedPeerRecord) == 0 {
		return nil, nil
	}
	env, _, err := record.ConsumeEnvelope(msg.SignedPeerRecord, peer.PeerRecordEnvelopeDomain)
	return env, err
}

// netNotifiee defines methods to be used with the IpfsDHT
type netNotifiee idService

func (nn *netNotifiee) IDService() *idService {
	return (*idService)(nn)
}

func (nn *netNotifiee) Connected(n network.Network, v network.Conn) {
	nn.IDService().IdentifyWait(v)
}

func (nn *netNotifiee) Disconnected(n network.Network, v network.Conn) {
	ids := nn.IDService()

	// Stop tracking the connection.
	ids.removeConn(v)

	// undo the setting of addresses to peer.ConnectedAddrTTL we did
	ids.addrMu.Lock()
	defer ids.addrMu.Unlock()

	if ids.Host.Network().Connectedness(v.RemotePeer()) != network.Connected {
		// consider removing the peer handler for this
		select {
		case ids.rmPeerHandlerCh <- rmPeerHandlerReq{v.RemotePeer()}:
		case <-ids.ctx.Done():
			return
		}

		// Last disconnect.
		ps := ids.Host.Peerstore()
		ps.UpdateAddrs(v.RemotePeer(), peerstore.ConnectedAddrTTL, peerstore.RecentlyConnectedAddrTTL)
	}
}

func (nn *netNotifiee) Listen(n network.Network, a ma.Multiaddr)      {}
func (nn *netNotifiee) ListenClose(n network.Network, a ma.Multiaddr) {}
