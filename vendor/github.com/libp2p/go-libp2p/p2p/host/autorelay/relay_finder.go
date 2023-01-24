package autorelay

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	basic "github.com/libp2p/go-libp2p/p2p/host/basic"
	relayv1 "github.com/libp2p/go-libp2p/p2p/protocol/circuitv1/relay"
	circuitv2 "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/client"
	circuitv2_proto "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/proto"

	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

const (
	protoIDv1 = string(relayv1.ProtoID)
	protoIDv2 = string(circuitv2_proto.ProtoIDv2Hop)
)

// Terminology:
// Candidate: Once we connect to a node and it supports (v1 / v2) relay protocol,
// we call it a candidate, and consider using it as a relay.
// Relay: Out of the list of candidates, we select a relay to connect to.
// Currently, we just randomly select a candidate, but we can employ more sophisticated
// selection strategies here (e.g. by facotring in the RTT).

const (
	rsvpRefreshInterval = time.Minute
	rsvpExpirationSlack = 2 * time.Minute

	autorelayTag = "autorelay"
)

type candidate struct {
	added           time.Time
	supportsRelayV2 bool
	ai              peer.AddrInfo
}

// relayFinder is a Host that uses relays for connectivity when a NAT is detected.
type relayFinder struct {
	bootTime time.Time
	host     *basic.BasicHost

	conf *config

	refCount sync.WaitGroup

	ctxCancel   context.CancelFunc
	ctxCancelMx sync.Mutex

	peerSource func(int) <-chan peer.AddrInfo

	candidateFound             chan struct{} // receives every time we find a new relay candidate
	candidateMx                sync.Mutex
	candidates                 map[peer.ID]*candidate
	backoff                    map[peer.ID]time.Time
	maybeConnectToRelayTrigger chan struct{} // cap: 1
	// Any time _something_ hapens that might cause us to need new candidates.
	// This could be
	// * the disconnection of a relay
	// * the failed attempt to obtain a reservation with a current candidate
	// * a candidate is deleted due to its age
	maybeRequestNewCandidates chan struct{} // cap: 1.

	relayUpdated chan struct{}

	relayMx sync.Mutex
	relays  map[peer.ID]*circuitv2.Reservation // rsvp will be nil if it is a v1 relay

	cachedAddrs       []ma.Multiaddr
	cachedAddrsExpiry time.Time
}

func newRelayFinder(host *basic.BasicHost, peerSource func(int) <-chan peer.AddrInfo, conf *config) *relayFinder {
	return &relayFinder{
		bootTime:                   conf.clock.Now(),
		host:                       host,
		conf:                       conf,
		peerSource:                 peerSource,
		candidates:                 make(map[peer.ID]*candidate),
		backoff:                    make(map[peer.ID]time.Time),
		candidateFound:             make(chan struct{}, 1),
		maybeConnectToRelayTrigger: make(chan struct{}, 1),
		maybeRequestNewCandidates:  make(chan struct{}, 1),
		relays:                     make(map[peer.ID]*circuitv2.Reservation),
		relayUpdated:               make(chan struct{}, 1),
	}
}

func (rf *relayFinder) background(ctx context.Context) {
	if rf.usesStaticRelay() {
		rf.refCount.Add(1)
		go func() {
			defer rf.refCount.Done()
			rf.handleStaticRelays(ctx)
		}()
	} else {
		rf.refCount.Add(1)
		go func() {
			defer rf.refCount.Done()
			rf.findNodes(ctx)
		}()
	}

	rf.refCount.Add(1)
	go func() {
		defer rf.refCount.Done()
		rf.handleNewCandidates(ctx)
	}()

	subConnectedness, err := rf.host.EventBus().Subscribe(new(event.EvtPeerConnectednessChanged))
	if err != nil {
		log.Error("failed to subscribe to the EvtPeerConnectednessChanged")
		return
	}
	defer subConnectedness.Close()

	bootDelayTimer := rf.conf.clock.Timer(rf.conf.bootDelay)
	defer bootDelayTimer.Stop()
	refreshTicker := rf.conf.clock.Ticker(rsvpRefreshInterval)
	defer refreshTicker.Stop()
	backoffTicker := rf.conf.clock.Ticker(rf.conf.backoff / 5)
	defer backoffTicker.Stop()
	oldCandidateTicker := rf.conf.clock.Ticker(rf.conf.maxCandidateAge / 5)
	defer oldCandidateTicker.Stop()

	for {
		// when true, we need to identify push
		var push bool

		select {
		case ev, ok := <-subConnectedness.Out():
			if !ok {
				return
			}
			evt := ev.(event.EvtPeerConnectednessChanged)
			if evt.Connectedness != network.NotConnected {
				continue
			}
			rf.relayMx.Lock()
			if rf.usingRelay(evt.Peer) { // we were disconnected from a relay
				log.Debugw("disconnected from relay", "id", evt.Peer)
				delete(rf.relays, evt.Peer)
				rf.notifyMaybeConnectToRelay()
				rf.notifyMaybeNeedNewCandidates()
				push = true
			}
			rf.relayMx.Unlock()
		case <-rf.candidateFound:
			rf.notifyMaybeConnectToRelay()
		case <-bootDelayTimer.C:
			rf.notifyMaybeConnectToRelay()
		case <-rf.relayUpdated:
			push = true
		case now := <-refreshTicker.C:
			push = rf.refreshReservations(ctx, now)
		case now := <-backoffTicker.C:
			rf.candidateMx.Lock()
			for id, t := range rf.backoff {
				if !t.Add(rf.conf.backoff).After(now) {
					log.Debugw("removing backoff for node", "id", id)
					delete(rf.backoff, id)
				}
			}
			rf.candidateMx.Unlock()
		case now := <-oldCandidateTicker.C:
			var deleted bool
			rf.candidateMx.Lock()
			for id, cand := range rf.candidates {
				if !cand.added.Add(rf.conf.maxCandidateAge).After(now) {
					deleted = true
					log.Debugw("deleting candidate due to age", "id", id)
					delete(rf.candidates, id)
				}
			}
			rf.candidateMx.Unlock()
			if deleted {
				rf.notifyMaybeNeedNewCandidates()
			}
		case <-ctx.Done():
			return
		}

		if push {
			rf.relayMx.Lock()
			rf.cachedAddrs = nil
			rf.relayMx.Unlock()
			rf.host.SignalAddressChange()
		}
	}
}

// findNodes accepts nodes from the channel and tests if they support relaying.
// It is run on both public and private nodes.
// It garbage collects old entries, so that nodes doesn't overflow.
// This makes sure that as soon as we need to find relay candidates, we have them available.
func (rf *relayFinder) findNodes(ctx context.Context) {
	peerChan := rf.peerSource(rf.conf.maxCandidates)
	var wg sync.WaitGroup
	lastCallToPeerSource := rf.conf.clock.Now()

	timer := newTimer(rf.conf.clock)
	for {
		rf.candidateMx.Lock()
		numCandidates := len(rf.candidates)
		rf.candidateMx.Unlock()

		if peerChan == nil {
			now := rf.conf.clock.Now()
			nextAllowedCallToPeerSource := lastCallToPeerSource.Add(rf.conf.minInterval).Sub(now)
			if numCandidates < rf.conf.minCandidates {
				log.Debugw("not enough candidates. Resetting timer", "num", numCandidates, "desired", rf.conf.minCandidates)
				timer.Reset(nextAllowedCallToPeerSource)
			}
		}

		select {
		case <-rf.maybeRequestNewCandidates:
			continue
		case now := <-timer.Chan():
			timer.SetRead()
			if peerChan != nil {
				// We're still reading peers from the peerChan. No need to query for more peers now.
				continue
			}
			lastCallToPeerSource = now
			peerChan = rf.peerSource(rf.conf.maxCandidates)
		case pi, ok := <-peerChan:
			if !ok {
				wg.Wait()
				peerChan = nil
				continue
			}
			log.Debugw("found node", "id", pi.ID)
			rf.candidateMx.Lock()
			numCandidates := len(rf.candidates)
			backoffStart, isOnBackoff := rf.backoff[pi.ID]
			rf.candidateMx.Unlock()
			if isOnBackoff {
				log.Debugw("skipping node that we recently failed to obtain a reservation with", "id", pi.ID, "last attempt", rf.conf.clock.Since(backoffStart))
				continue
			}
			if numCandidates >= rf.conf.maxCandidates {
				log.Debugw("skipping node. Already have enough candidates", "id", pi.ID, "num", numCandidates, "max", rf.conf.maxCandidates)
				continue
			}
			rf.refCount.Add(1)
			wg.Add(1)
			go func() {
				defer rf.refCount.Done()
				defer wg.Done()
				if added := rf.handleNewNode(ctx, pi); added {
					rf.notifyNewCandidate()
				}
			}()
		case <-ctx.Done():
			return
		}
	}
}

func (rf *relayFinder) handleStaticRelays(ctx context.Context) {
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	wg.Add(len(rf.conf.staticRelays))
	for _, pi := range rf.conf.staticRelays {
		sem <- struct{}{}
		go func(pi peer.AddrInfo) {
			defer wg.Done()
			defer func() { <-sem }()
			rf.handleNewNode(ctx, pi)
		}(pi)
	}
	wg.Wait()
	log.Debug("processed all static relays")
	rf.notifyNewCandidate()
}

func (rf *relayFinder) notifyMaybeConnectToRelay() {
	select {
	case rf.maybeConnectToRelayTrigger <- struct{}{}:
	default:
	}
}

func (rf *relayFinder) notifyMaybeNeedNewCandidates() {
	select {
	case rf.maybeRequestNewCandidates <- struct{}{}:
	default:
	}
}

func (rf *relayFinder) notifyNewCandidate() {
	select {
	case rf.candidateFound <- struct{}{}:
	default:
	}
}

// handleNewNode tests if a peer supports circuit v1 or v2.
// This method is only run on private nodes.
// If a peer does, it is added to the candidates map.
// Note that just supporting the protocol doesn't guarantee that we can also obtain a reservation.
func (rf *relayFinder) handleNewNode(ctx context.Context, pi peer.AddrInfo) (added bool) {
	rf.relayMx.Lock()
	relayInUse := rf.usingRelay(pi.ID)
	rf.relayMx.Unlock()
	if relayInUse {
		return false
	}

	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	supportsV2, err := rf.tryNode(ctx, pi)
	if err != nil {
		log.Debugf("node %s not accepted as a candidate: %s", pi.ID, err)
		return false
	}
	rf.candidateMx.Lock()
	if len(rf.candidates) > rf.conf.maxCandidates {
		rf.candidateMx.Unlock()
		return false
	}
	log.Debugw("node supports relay protocol", "peer", pi.ID, "supports circuit v2", supportsV2)
	rf.candidates[pi.ID] = &candidate{
		added:           rf.conf.clock.Now(),
		ai:              pi,
		supportsRelayV2: supportsV2,
	}
	rf.candidateMx.Unlock()
	return true
}

// tryNode checks if a peer actually supports either circuit v1 or circuit v2.
// It does not modify any internal state.
func (rf *relayFinder) tryNode(ctx context.Context, pi peer.AddrInfo) (supportsRelayV2 bool, err error) {
	if err := rf.host.Connect(ctx, pi); err != nil {
		return false, fmt.Errorf("error connecting to relay %s: %w", pi.ID, err)
	}

	conns := rf.host.Network().ConnsToPeer(pi.ID)
	for _, conn := range conns {
		if isRelayAddr(conn.RemoteMultiaddr()) {
			return false, errors.New("not a public node")
		}
	}

	// wait for identify to complete in at least one conn so that we can check the supported protocols
	ready := make(chan struct{}, 1)
	for _, conn := range conns {
		go func(conn network.Conn) {
			select {
			case <-rf.host.IDService().IdentifyWait(conn):
				select {
				case ready <- struct{}{}:
				default:
				}
			case <-ctx.Done():
			}
		}(conn)
	}

	select {
	case <-ready:
	case <-ctx.Done():
		return false, ctx.Err()
	}

	protos, err := rf.host.Peerstore().SupportsProtocols(pi.ID, protoIDv1, protoIDv2)
	if err != nil {
		return false, fmt.Errorf("error checking relay protocol support for peer %s: %w", pi.ID, err)
	}

	// If the node speaks both, prefer circuit v2
	var maybeSupportsV1, supportsV2 bool
	for _, proto := range protos {
		switch proto {
		case protoIDv1:
			maybeSupportsV1 = true
		case protoIDv2:
			supportsV2 = true
		}
	}

	if supportsV2 {
		return true, nil
	}

	if !rf.conf.enableCircuitV1 && !supportsV2 {
		return false, errors.New("doesn't speak circuit v2")
	}
	if !maybeSupportsV1 && !supportsV2 {
		return false, errors.New("doesn't speak circuit v1 or v2")
	}

	// The node *may* support circuit v1.
	supportsV1, err := relayv1.CanHop(ctx, rf.host, pi.ID)
	if err != nil {
		return false, fmt.Errorf("CanHop failed: %w", err)
	}
	if !supportsV1 {
		return false, errors.New("doesn't speak circuit v1 or v2")
	}
	return false, nil
}

// When a new node that could be a relay is found, we receive a notification on the maybeConnectToRelayTrigger chan.
// This function makes sure that we only run one instance of maybeConnectToRelay at once, and buffers
// exactly one more trigger event to run maybeConnectToRelay.
func (rf *relayFinder) handleNewCandidates(ctx context.Context) {
	sem := make(chan struct{}, 1)
	for {
		select {
		case <-ctx.Done():
			return
		case <-rf.maybeConnectToRelayTrigger:
			select {
			case <-ctx.Done():
				return
			case sem <- struct{}{}:
			}
			rf.maybeConnectToRelay(ctx)
			<-sem
		}
	}
}

func (rf *relayFinder) maybeConnectToRelay(ctx context.Context) {
	rf.relayMx.Lock()
	numRelays := len(rf.relays)
	rf.relayMx.Unlock()
	// We're already connected to our desired number of relays. Nothing to do here.
	if numRelays == rf.conf.desiredRelays {
		return
	}

	rf.candidateMx.Lock()
	if !rf.usesStaticRelay() && len(rf.relays) == 0 && len(rf.candidates) < rf.conf.minCandidates && rf.conf.clock.Since(rf.bootTime) < rf.conf.bootDelay {
		// During the startup phase, we don't want to connect to the first candidate that we find.
		// Instead, we wait until we've found at least minCandidates, and then select the best of those.
		// However, if that takes too long (longer than bootDelay), we still go ahead.
		rf.candidateMx.Unlock()
		return
	}
	if len(rf.candidates) == 0 {
		rf.candidateMx.Unlock()
		return
	}
	candidates := rf.selectCandidates()
	rf.candidateMx.Unlock()

	// We now iterate over the candidates, attempting (sequentially) to get reservations with them, until
	// we reach the desired number of relays.
	for _, cand := range candidates {
		id := cand.ai.ID
		rf.relayMx.Lock()
		usingRelay := rf.usingRelay(id)
		rf.relayMx.Unlock()
		if usingRelay {
			rf.candidateMx.Lock()
			delete(rf.candidates, id)
			rf.candidateMx.Unlock()
			rf.notifyMaybeNeedNewCandidates()
			continue
		}
		rsvp, err := rf.connectToRelay(ctx, cand)
		if err != nil {
			log.Debugw("failed to connect to relay", "peer", id, "error", err)
			rf.notifyMaybeNeedNewCandidates()
			continue
		}
		log.Debugw("adding new relay", "id", id)
		rf.relayMx.Lock()
		rf.relays[id] = rsvp
		numRelays := len(rf.relays)
		rf.relayMx.Unlock()
		rf.notifyMaybeNeedNewCandidates()

		rf.host.ConnManager().Protect(id, autorelayTag) // protect the connection

		select {
		case rf.relayUpdated <- struct{}{}:
		default:
		}

		if numRelays >= rf.conf.desiredRelays {
			break
		}
	}
}

func (rf *relayFinder) connectToRelay(ctx context.Context, cand *candidate) (*circuitv2.Reservation, error) {
	id := cand.ai.ID

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	var rsvp *circuitv2.Reservation

	// make sure we're still connected.
	if rf.host.Network().Connectedness(id) != network.Connected {
		if err := rf.host.Connect(ctx, cand.ai); err != nil {
			rf.candidateMx.Lock()
			delete(rf.candidates, cand.ai.ID)
			rf.candidateMx.Unlock()
			return nil, fmt.Errorf("failed to connect: %w", err)
		}
	}

	rf.candidateMx.Lock()
	rf.backoff[id] = rf.conf.clock.Now()
	rf.candidateMx.Unlock()
	var err error
	if cand.supportsRelayV2 {
		rsvp, err = circuitv2.Reserve(ctx, rf.host, cand.ai)
		if err != nil {
			err = fmt.Errorf("failed to reserve slot: %w", err)
		}
	}
	rf.candidateMx.Lock()
	delete(rf.candidates, id)
	rf.candidateMx.Unlock()
	return rsvp, err
}

func (rf *relayFinder) refreshReservations(ctx context.Context, now time.Time) bool {
	rf.relayMx.Lock()

	// find reservations about to expire and refresh them in parallel
	g := new(errgroup.Group)
	for p, rsvp := range rf.relays {
		if rsvp == nil { // this is a circuit v1 relay, there is no reservation
			continue
		}
		if now.Add(rsvpExpirationSlack).Before(rsvp.Expiration) {
			continue
		}

		p := p
		g.Go(func() error { return rf.refreshRelayReservation(ctx, p) })
	}
	rf.relayMx.Unlock()

	err := g.Wait()
	return err != nil
}

func (rf *relayFinder) refreshRelayReservation(ctx context.Context, p peer.ID) error {
	rsvp, err := circuitv2.Reserve(ctx, rf.host, peer.AddrInfo{ID: p})

	rf.relayMx.Lock()
	defer rf.relayMx.Unlock()

	if err != nil {
		log.Debugw("failed to refresh relay slot reservation", "relay", p, "error", err)

		delete(rf.relays, p)
		// unprotect the connection
		rf.host.ConnManager().Unprotect(p, autorelayTag)
		return err
	}

	log.Debugw("refreshed relay slot reservation", "relay", p)
	rf.relays[p] = rsvp
	return nil
}

// usingRelay returns if we're currently using the given relay.
func (rf *relayFinder) usingRelay(p peer.ID) bool {
	_, ok := rf.relays[p]
	return ok
}

// selectCandidates returns an ordered slice of relay candidates.
// Callers should attempt to obtain reservations with the candidates in this order.
func (rf *relayFinder) selectCandidates() []*candidate {
	candidates := make([]*candidate, 0, len(rf.candidates))
	for _, cand := range rf.candidates {
		candidates = append(candidates, cand)
	}

	// TODO: better relay selection strategy; this just selects random relays,
	// but we should probably use ping latency as the selection metric
	rand.Shuffle(len(candidates), func(i, j int) {
		candidates[i], candidates[j] = candidates[j], candidates[i]
	})
	return candidates
}

// This function is computes the NATed relay addrs when our status is private:
//   - The public addrs are removed from the address set.
//   - The non-public addrs are included verbatim so that peers behind the same NAT/firewall
//     can still dial us directly.
//   - On top of those, we add the relay-specific addrs for the relays to which we are
//     connected. For each non-private relay addr, we encapsulate the p2p-circuit addr
//     through which we can be dialed.
func (rf *relayFinder) relayAddrs(addrs []ma.Multiaddr) []ma.Multiaddr {
	rf.relayMx.Lock()
	defer rf.relayMx.Unlock()

	if rf.cachedAddrs != nil && rf.conf.clock.Now().Before(rf.cachedAddrsExpiry) {
		return rf.cachedAddrs
	}

	raddrs := make([]ma.Multiaddr, 0, 4*len(rf.relays)+4)

	// only keep private addrs from the original addr set
	for _, addr := range addrs {
		if manet.IsPrivateAddr(addr) {
			raddrs = append(raddrs, addr)
		}
	}

	// add relay specific addrs to the list
	for p := range rf.relays {
		addrs := cleanupAddressSet(rf.host.Peerstore().Addrs(p))

		circuit := ma.StringCast(fmt.Sprintf("/p2p/%s/p2p-circuit", p.Pretty()))
		for _, addr := range addrs {
			pub := addr.Encapsulate(circuit)
			raddrs = append(raddrs, pub)
		}
	}

	rf.cachedAddrs = raddrs
	rf.cachedAddrsExpiry = rf.conf.clock.Now().Add(30 * time.Second)

	return raddrs
}

func (rf *relayFinder) usesStaticRelay() bool {
	return len(rf.conf.staticRelays) > 0
}

func (rf *relayFinder) Start() error {
	rf.ctxCancelMx.Lock()
	defer rf.ctxCancelMx.Unlock()
	if rf.ctxCancel != nil {
		return errors.New("relayFinder already running")
	}
	log.Debug("starting relay finder")
	ctx, cancel := context.WithCancel(context.Background())
	rf.ctxCancel = cancel
	rf.refCount.Add(1)
	go func() {
		defer rf.refCount.Done()
		rf.background(ctx)
	}()
	return nil
}

func (rf *relayFinder) Stop() error {
	rf.ctxCancelMx.Lock()
	defer rf.ctxCancelMx.Unlock()
	log.Debug("stopping relay finder")
	if rf.ctxCancel != nil {
		rf.ctxCancel()
	}
	rf.refCount.Wait()
	rf.ctxCancel = nil
	return nil
}
