package identify

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/p2p/host/eventbus"

	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

// ActivationThresh sets how many times an address must be seen as "activated"
// and therefore advertised to other peers as an address that the local peer
// can be contacted on. The "seen" events expire by default after 40 minutes
// (OwnObservedAddressTTL * ActivationThreshold). The are cleaned up during
// the GC rounds set by GCInterval.
var ActivationThresh = 4

// GCInterval specicies how often to make a round cleaning seen events and
// observed addresses. An address will be cleaned if it has not been seen in
// OwnObservedAddressTTL (10 minutes). A "seen" event will be cleaned up if
// it is older than OwnObservedAddressTTL * ActivationThresh (40 minutes).
var GCInterval = 10 * time.Minute

// observedAddrManagerWorkerChannelSize defines how many addresses can be enqueued
// for adding to an ObservedAddrManager.
var observedAddrManagerWorkerChannelSize = 16

// maxObservedAddrsPerIPAndTransport is the maximum number of observed addresses
// we will return for each (IPx/TCP or UDP) group.
var maxObservedAddrsPerIPAndTransport = 2

// observation records an address observation from an "observer" (where every IP
// address is a unique observer).
type observation struct {
	// seenTime is the last time this observation was made.
	seenTime time.Time
	// inbound indicates whether or not this observation has been made from
	// an inbound connection. This remains true even if we an observation
	// from a subsequent outbound connection.
	inbound bool
}

// observedAddr is an entry for an address reported by our peers.
// We only use addresses that:
//   - have been observed at least 4 times in last 40 minutes. (counter symmetric nats)
//   - have been observed at least once recently (10 minutes), because our position in the
//     network, or network port mapppings, may have changed.
type observedAddr struct {
	addr       ma.Multiaddr
	seenBy     map[string]observation // peer(observer) address -> observation info
	lastSeen   time.Time
	numInbound int
}

func (oa *observedAddr) activated() bool {

	// We only activate if other peers observed the same address
	// of ours at least 4 times. SeenBy peers are removed by GC if
	// they say the address more than ttl*ActivationThresh
	return len(oa.seenBy) >= ActivationThresh
}

// GroupKey returns the group in which this observation belongs. Currently, an
// observed address's group is just the address with all ports set to 0. This
// means we can advertise the most commonly observed external ports without
// advertising _every_ observed port.
func (oa *observedAddr) groupKey() string {
	key := make([]byte, 0, len(oa.addr.Bytes()))
	ma.ForEach(oa.addr, func(c ma.Component) bool {
		switch proto := c.Protocol(); proto.Code {
		case ma.P_TCP, ma.P_UDP:
			key = append(key, proto.VCode...)
			key = append(key, 0, 0) // zero in two bytes
		default:
			key = append(key, c.Bytes()...)
		}
		return true
	})

	return string(key)
}

type newObservation struct {
	conn     network.Conn
	observed ma.Multiaddr
}

// ObservedAddrManager keeps track of a ObservedAddrs.
type ObservedAddrManager struct {
	host host.Host

	closeOnce sync.Once
	refCount  sync.WaitGroup
	ctx       context.Context // the context is canceled when Close is called
	ctxCancel context.CancelFunc

	// latest observation from active connections
	// we'll "re-observe" these when we gc
	activeConnsMu sync.Mutex
	// active connection -> most recent observation
	activeConns map[network.Conn]ma.Multiaddr

	mu     sync.RWMutex
	closed bool
	// local(internal) address -> list of observed(external) addresses
	addrs        map[string][]*observedAddr
	ttl          time.Duration
	refreshTimer *time.Timer

	// this is the worker channel
	wch chan newObservation

	reachabilitySub event.Subscription
	reachability    network.Reachability

	currentUDPNATDeviceType  network.NATDeviceType
	currentTCPNATDeviceType  network.NATDeviceType
	emitNATDeviceTypeChanged event.Emitter
}

// NewObservedAddrManager returns a new address manager using
// peerstore.OwnObservedAddressTTL as the TTL.
func NewObservedAddrManager(host host.Host) (*ObservedAddrManager, error) {
	oas := &ObservedAddrManager{
		addrs:       make(map[string][]*observedAddr),
		ttl:         peerstore.OwnObservedAddrTTL,
		wch:         make(chan newObservation, observedAddrManagerWorkerChannelSize),
		host:        host,
		activeConns: make(map[network.Conn]ma.Multiaddr),
		// refresh every ttl/2 so we don't forget observations from connected peers
		refreshTimer: time.NewTimer(peerstore.OwnObservedAddrTTL / 2),
	}
	oas.ctx, oas.ctxCancel = context.WithCancel(context.Background())

	reachabilitySub, err := host.EventBus().Subscribe(new(event.EvtLocalReachabilityChanged))
	if err != nil {
		return nil, fmt.Errorf("failed to subscribe to reachability event: %s", err)
	}
	oas.reachabilitySub = reachabilitySub

	emitter, err := host.EventBus().Emitter(new(event.EvtNATDeviceTypeChanged), eventbus.Stateful)
	if err != nil {
		return nil, fmt.Errorf("failed to create emitter for NATDeviceType: %s", err)
	}
	oas.emitNATDeviceTypeChanged = emitter

	oas.host.Network().Notify((*obsAddrNotifiee)(oas))
	oas.refCount.Add(1)
	go oas.worker()
	return oas, nil
}

// AddrsFor return all activated observed addresses associated with the given
// (resolved) listen address.
func (oas *ObservedAddrManager) AddrsFor(addr ma.Multiaddr) (addrs []ma.Multiaddr) {
	oas.mu.RLock()
	defer oas.mu.RUnlock()

	if len(oas.addrs) == 0 {
		return nil
	}

	observedAddrs, ok := oas.addrs[string(addr.Bytes())]
	if !ok {
		return
	}

	return oas.filter(observedAddrs)
}

// Addrs return all activated observed addresses
func (oas *ObservedAddrManager) Addrs() []ma.Multiaddr {
	oas.mu.RLock()
	defer oas.mu.RUnlock()

	if len(oas.addrs) == 0 {
		return nil
	}

	var allObserved []*observedAddr
	for _, addrs := range oas.addrs {
		allObserved = append(allObserved, addrs...)
	}
	return oas.filter(allObserved)
}

func (oas *ObservedAddrManager) filter(observedAddrs []*observedAddr) []ma.Multiaddr {
	pmap := make(map[string][]*observedAddr)
	now := time.Now()

	for i := range observedAddrs {
		a := observedAddrs[i]
		if now.Sub(a.lastSeen) <= oas.ttl && a.activated() {
			// group addresses by their IPX/Transport Protocol(TCP or UDP) pattern.
			pat := a.groupKey()
			pmap[pat] = append(pmap[pat], a)

		}
	}

	addrs := make([]ma.Multiaddr, 0, len(observedAddrs))
	for pat := range pmap {
		s := pmap[pat]

		// We prefer inbound connection observations over outbound.
		// For ties, we prefer the ones with more votes.
		sort.Slice(s, func(i int, j int) bool {
			first := s[i]
			second := s[j]

			if first.numInbound > second.numInbound {
				return true
			}

			return len(first.seenBy) > len(second.seenBy)
		})

		for i := 0; i < maxObservedAddrsPerIPAndTransport && i < len(s); i++ {
			addrs = append(addrs, s[i].addr)
		}
	}

	return addrs
}

// Record records an address observation, if valid.
func (oas *ObservedAddrManager) Record(conn network.Conn, observed ma.Multiaddr) {
	select {
	case oas.wch <- newObservation{
		conn:     conn,
		observed: observed,
	}:
	default:
		log.Debugw("dropping address observation due to full buffer",
			"from", conn.RemoteMultiaddr(),
			"observed", observed,
		)
	}
}

func (oas *ObservedAddrManager) worker() {
	defer oas.refCount.Done()

	ticker := time.NewTicker(GCInterval)
	defer ticker.Stop()

	subChan := oas.reachabilitySub.Out()
	for {
		select {
		case evt, ok := <-subChan:
			if !ok {
				subChan = nil
				continue
			}
			ev := evt.(event.EvtLocalReachabilityChanged)
			oas.reachability = ev.Reachability
		case obs := <-oas.wch:
			oas.maybeRecordObservation(obs.conn, obs.observed)
		case <-ticker.C:
			oas.gc()
		case <-oas.refreshTimer.C:
			oas.refresh()
		case <-oas.ctx.Done():
			return
		}
	}
}

func (oas *ObservedAddrManager) refresh() {
	oas.activeConnsMu.Lock()
	recycledObservations := make([]newObservation, 0, len(oas.activeConns))
	for conn, observed := range oas.activeConns {
		recycledObservations = append(recycledObservations, newObservation{
			conn:     conn,
			observed: observed,
		})
	}
	oas.activeConnsMu.Unlock()

	oas.mu.Lock()
	defer oas.mu.Unlock()
	for _, obs := range recycledObservations {
		oas.recordObservationUnlocked(obs.conn, obs.observed)
	}
	// refresh every ttl/2 so we don't forget observations from connected peers
	oas.refreshTimer.Reset(oas.ttl / 2)
}

func (oas *ObservedAddrManager) gc() {
	oas.mu.Lock()
	defer oas.mu.Unlock()

	now := time.Now()
	for local, observedAddrs := range oas.addrs {
		filteredAddrs := observedAddrs[:0]
		for _, a := range observedAddrs {
			// clean up SeenBy set
			for k, ob := range a.seenBy {
				if now.Sub(ob.seenTime) > oas.ttl*time.Duration(ActivationThresh) {
					delete(a.seenBy, k)
					if ob.inbound {
						a.numInbound--
					}
				}
			}

			// leave only alive observed addresses
			if now.Sub(a.lastSeen) <= oas.ttl {
				filteredAddrs = append(filteredAddrs, a)
			}
		}
		if len(filteredAddrs) > 0 {
			oas.addrs[local] = filteredAddrs
		} else {
			delete(oas.addrs, local)
		}
	}
}

func (oas *ObservedAddrManager) addConn(conn network.Conn, observed ma.Multiaddr) {
	oas.activeConnsMu.Lock()
	defer oas.activeConnsMu.Unlock()

	// We need to make sure we haven't received a disconnect event for this
	// connection yet. The only way to do that right now is to make sure the
	// swarm still has the connection.
	//
	// Doing this under a lock that we _also_ take in a disconnect event
	// handler ensures everything happens in the right order.
	for _, c := range oas.host.Network().ConnsToPeer(conn.RemotePeer()) {
		if c == conn {
			oas.activeConns[conn] = observed
			return
		}
	}
}

func (oas *ObservedAddrManager) removeConn(conn network.Conn) {
	// DO NOT remove this lock.
	// This ensures we don't call addConn at the same time:
	// 1. see that we have a connection and pause inside addConn right before recording it.
	// 2. process a disconnect event.
	// 3. record the connection (leaking it).

	oas.activeConnsMu.Lock()
	delete(oas.activeConns, conn)
	oas.activeConnsMu.Unlock()
}

func (oas *ObservedAddrManager) maybeRecordObservation(conn network.Conn, observed ma.Multiaddr) {
	// First, determine if this observation is even worth keeping...

	// Ignore observations from loopback nodes. We already know our loopback
	// addresses.
	if manet.IsIPLoopback(observed) {
		return
	}

	// we should only use ObservedAddr when our connection's LocalAddr is one
	// of our ListenAddrs. If we Dial out using an ephemeral addr, knowing that
	// address's external mapping is not very useful because the port will not be
	// the same as the listen addr.
	ifaceaddrs, err := oas.host.Network().InterfaceListenAddresses()
	if err != nil {
		log.Infof("failed to get interface listen addrs", err)
		return
	}

	local := conn.LocalMultiaddr()
	if !ma.Contains(ifaceaddrs, local) && !ma.Contains(oas.host.Network().ListenAddresses(), local) {
		// not in our list
		return
	}

	// We should reject the connection if the observation doesn't match the
	// transports of one of our advertised addresses.
	if !HasConsistentTransport(observed, oas.host.Addrs()) &&
		!HasConsistentTransport(observed, oas.host.Network().ListenAddresses()) {
		log.Debugw(
			"observed multiaddr doesn't match the transports of any announced addresses",
			"from", conn.RemoteMultiaddr(),
			"observed", observed,
		)
		return
	}

	// Ok, the observation is good, record it.
	log.Debugw("added own observed listen addr", "observed", observed)

	defer oas.addConn(conn, observed)

	oas.mu.Lock()
	defer oas.mu.Unlock()
	oas.recordObservationUnlocked(conn, observed)

	if oas.reachability == network.ReachabilityPrivate {
		oas.emitAllNATTypes()
	}
}

func (oas *ObservedAddrManager) recordObservationUnlocked(conn network.Conn, observed ma.Multiaddr) {
	now := time.Now()
	observerString := observerGroup(conn.RemoteMultiaddr())
	localString := string(conn.LocalMultiaddr().Bytes())
	ob := observation{
		seenTime: now,
		inbound:  conn.Stat().Direction == network.DirInbound,
	}

	// check if observed address seen yet, if so, update it
	for _, observedAddr := range oas.addrs[localString] {
		if observedAddr.addr.Equal(observed) {
			// Don't trump an outbound observation with an inbound
			// one.
			wasInbound := observedAddr.seenBy[observerString].inbound
			isInbound := ob.inbound
			ob.inbound = isInbound || wasInbound

			if !wasInbound && isInbound {
				observedAddr.numInbound++
			}

			observedAddr.seenBy[observerString] = ob
			observedAddr.lastSeen = now
			return
		}
	}

	// observed address not seen yet, append it
	oa := &observedAddr{
		addr: observed,
		seenBy: map[string]observation{
			observerString: ob,
		},
		lastSeen: now,
	}
	if ob.inbound {
		oa.numInbound++
	}
	oas.addrs[localString] = append(oas.addrs[localString], oa)
}

// For a given transport Protocol (TCP/UDP):
//
// 1. If we have an activated address, we are behind an Cone NAT.
// With regards to RFC 3489, this could be either a Full Cone NAT, a Restricted Cone NAT or a
// Port Restricted Cone NAT. However, we do NOT differentiate between them here and simply classify all such NATs as a Cone NAT.
//
// 2. If four different peers observe a different address for us on outbound connections, we
// are MOST probably behind a Symmetric NAT.
//
// Please see the documentation on the enumerations for `network.NATDeviceType` for more details about these NAT Device types
// and how they relate to NAT traversal via Hole Punching.
func (oas *ObservedAddrManager) emitAllNATTypes() {
	var allObserved []*observedAddr
	for _, addrs := range oas.addrs {
		allObserved = append(allObserved, addrs...)
	}

	hasChanged, natType := oas.emitSpecificNATType(allObserved, ma.P_TCP, network.NATTransportTCP, oas.currentTCPNATDeviceType)
	if hasChanged {
		oas.currentTCPNATDeviceType = natType
	}

	hasChanged, natType = oas.emitSpecificNATType(allObserved, ma.P_UDP, network.NATTransportUDP, oas.currentUDPNATDeviceType)
	if hasChanged {
		oas.currentUDPNATDeviceType = natType
	}
}

// returns true along with the new NAT device type if the NAT device type for the given protocol has changed.
// returns false otherwise.
func (oas *ObservedAddrManager) emitSpecificNATType(addrs []*observedAddr, protoCode int, transportProto network.NATTransportProtocol,
	currentNATType network.NATDeviceType) (bool, network.NATDeviceType) {
	now := time.Now()
	seenBy := make(map[string]struct{})
	cnt := 0

	for _, oa := range addrs {
		_, err := oa.addr.ValueForProtocol(protoCode)
		if err != nil {
			continue
		}

		// if we have an activated addresses, it's a Cone NAT.
		if now.Sub(oa.lastSeen) <= oas.ttl && oa.activated() {
			if currentNATType != network.NATDeviceTypeCone {
				oas.emitNATDeviceTypeChanged.Emit(event.EvtNATDeviceTypeChanged{
					TransportProtocol: transportProto,
					NatDeviceType:     network.NATDeviceTypeCone,
				})
				return true, network.NATDeviceTypeCone
			}

			// our current NAT Device Type is already CONE, nothing to do here.
			return false, 0
		}

		// An observed address on an outbound connection that has ONLY been seen by one peer
		if now.Sub(oa.lastSeen) <= oas.ttl && oa.numInbound == 0 && len(oa.seenBy) == 1 {
			cnt++
			for s := range oa.seenBy {
				seenBy[s] = struct{}{}
			}
		}
	}

	// If four different peers observe a different address for us on each of four outbound connections, we
	// are MOST probably behind a Symmetric NAT.
	if cnt >= ActivationThresh && len(seenBy) >= ActivationThresh {
		if currentNATType != network.NATDeviceTypeSymmetric {
			oas.emitNATDeviceTypeChanged.Emit(event.EvtNATDeviceTypeChanged{
				TransportProtocol: transportProto,
				NatDeviceType:     network.NATDeviceTypeSymmetric,
			})
			return true, network.NATDeviceTypeSymmetric
		}
	}

	return false, 0
}

func (oas *ObservedAddrManager) Close() error {
	oas.closeOnce.Do(func() {
		oas.ctxCancel()

		oas.mu.Lock()
		oas.closed = true
		oas.refreshTimer.Stop()
		oas.mu.Unlock()

		oas.refCount.Wait()
		oas.reachabilitySub.Close()
		oas.host.Network().StopNotify((*obsAddrNotifiee)(oas))
	})
	return nil
}

// observerGroup is a function that determines what part of
// a multiaddr counts as a different observer. for example,
// two ipfs nodes at the same IP/TCP transport would get
// the exact same NAT mapping; they would count as the
// same observer. This may protect against NATs who assign
// different ports to addresses at different IP hosts, but
// not TCP ports.
//
// Here, we use the root multiaddr address. This is mostly
// IP addresses. In practice, this is what we want.
func observerGroup(m ma.Multiaddr) string {
	// TODO: If IPv6 rolls out we should mark /64 routing zones as one group
	first, _ := ma.SplitFirst(m)
	return string(first.Bytes())
}

// SetTTL sets the TTL of an observed address manager.
func (oas *ObservedAddrManager) SetTTL(ttl time.Duration) {
	oas.mu.Lock()
	defer oas.mu.Unlock()
	if oas.closed {
		return
	}
	oas.ttl = ttl
	// refresh every ttl/2 so we don't forget observations from connected peers
	oas.refreshTimer.Reset(ttl / 2)
}

// TTL gets the TTL of an observed address manager.
func (oas *ObservedAddrManager) TTL() time.Duration {
	oas.mu.RLock()
	defer oas.mu.RUnlock()
	return oas.ttl
}

type obsAddrNotifiee ObservedAddrManager

func (on *obsAddrNotifiee) Listen(n network.Network, a ma.Multiaddr)      {}
func (on *obsAddrNotifiee) ListenClose(n network.Network, a ma.Multiaddr) {}
func (on *obsAddrNotifiee) Connected(n network.Network, v network.Conn)   {}
func (on *obsAddrNotifiee) Disconnected(n network.Network, v network.Conn) {
	(*ObservedAddrManager)(on).removeConn(v)
}
