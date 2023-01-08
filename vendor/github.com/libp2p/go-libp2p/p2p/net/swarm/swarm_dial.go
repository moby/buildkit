package swarm

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/canonicallog"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/transport"

	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

// Diagram of dial sync:
//
//   many callers of Dial()   synched w.  dials many addrs       results to callers
//  ----------------------\    dialsync    use earliest            /--------------
//  -----------------------\              |----------\           /----------------
//  ------------------------>------------<-------     >---------<-----------------
//  -----------------------|              \----x                 \----------------
//  ----------------------|                \-----x                \---------------
//                                         any may fail          if no addr at end
//                                                             retry dialAttempt x

var (
	// ErrDialBackoff is returned by the backoff code when a given peer has
	// been dialed too frequently
	ErrDialBackoff = errors.New("dial backoff")

	// ErrDialToSelf is returned if we attempt to dial our own peer
	ErrDialToSelf = errors.New("dial to self attempted")

	// ErrNoTransport is returned when we don't know a transport for the
	// given multiaddr.
	ErrNoTransport = errors.New("no transport for protocol")

	// ErrAllDialsFailed is returned when connecting to a peer has ultimately failed
	ErrAllDialsFailed = errors.New("all dials failed")

	// ErrNoAddresses is returned when we fail to find any addresses for a
	// peer we're trying to dial.
	ErrNoAddresses = errors.New("no addresses")

	// ErrNoGoodAddresses is returned when we find addresses for a peer but
	// can't use any of them.
	ErrNoGoodAddresses = errors.New("no good addresses")

	// ErrGaterDisallowedConnection is returned when the gater prevents us from
	// forming a connection with a peer.
	ErrGaterDisallowedConnection = errors.New("gater disallows connection to peer")
)

// DialAttempts governs how many times a goroutine will try to dial a given peer.
// Note: this is down to one, as we have _too many dials_ atm. To add back in,
// add loop back in Dial(.)
const DialAttempts = 1

// ConcurrentFdDials is the number of concurrent outbound dials over transports
// that consume file descriptors
const ConcurrentFdDials = 160

// DefaultPerPeerRateLimit is the number of concurrent outbound dials to make
// per peer
var DefaultPerPeerRateLimit = 8

// dialbackoff is a struct used to avoid over-dialing the same, dead peers.
// Whenever we totally time out on a peer (all three attempts), we add them
// to dialbackoff. Then, whenevers goroutines would _wait_ (dialsync), they
// check dialbackoff. If it's there, they don't wait and exit promptly with
// an error. (the single goroutine that is actually dialing continues to
// dial). If a dial is successful, the peer is removed from backoff.
// Example:
//
//  for {
//  	if ok, wait := dialsync.Lock(p); !ok {
//  		if backoff.Backoff(p) {
//  			return errDialFailed
//  		}
//  		<-wait
//  		continue
//  	}
//  	defer dialsync.Unlock(p)
//  	c, err := actuallyDial(p)
//  	if err != nil {
//  		dialbackoff.AddBackoff(p)
//  		continue
//  	}
//  	dialbackoff.Clear(p)
//  }
//

// DialBackoff is a type for tracking peer dial backoffs.
//
// * It's safe to use its zero value.
// * It's thread-safe.
// * It's *not* safe to move this type after using.
type DialBackoff struct {
	entries map[peer.ID]map[string]*backoffAddr
	lock    sync.RWMutex
}

type backoffAddr struct {
	tries int
	until time.Time
}

func (db *DialBackoff) init(ctx context.Context) {
	if db.entries == nil {
		db.entries = make(map[peer.ID]map[string]*backoffAddr)
	}
	go db.background(ctx)
}

func (db *DialBackoff) background(ctx context.Context) {
	ticker := time.NewTicker(BackoffMax)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			db.cleanup()
		}
	}
}

// Backoff returns whether the client should backoff from dialing
// peer p at address addr
func (db *DialBackoff) Backoff(p peer.ID, addr ma.Multiaddr) (backoff bool) {
	db.lock.Lock()
	defer db.lock.Unlock()

	ap, found := db.entries[p][string(addr.Bytes())]
	return found && time.Now().Before(ap.until)
}

// BackoffBase is the base amount of time to backoff (default: 5s).
var BackoffBase = time.Second * 5

// BackoffCoef is the backoff coefficient (default: 1s).
var BackoffCoef = time.Second

// BackoffMax is the maximum backoff time (default: 5m).
var BackoffMax = time.Minute * 5

// AddBackoff lets other nodes know that we've entered backoff with
// peer p, so dialers should not wait unnecessarily. We still will
// attempt to dial with one goroutine, in case we get through.
//
// Backoff is not exponential, it's quadratic and computed according to the
// following formula:
//
//	BackoffBase + BakoffCoef * PriorBackoffs^2
//
// Where PriorBackoffs is the number of previous backoffs.
func (db *DialBackoff) AddBackoff(p peer.ID, addr ma.Multiaddr) {
	saddr := string(addr.Bytes())
	db.lock.Lock()
	defer db.lock.Unlock()
	bp, ok := db.entries[p]
	if !ok {
		bp = make(map[string]*backoffAddr, 1)
		db.entries[p] = bp
	}
	ba, ok := bp[saddr]
	if !ok {
		bp[saddr] = &backoffAddr{
			tries: 1,
			until: time.Now().Add(BackoffBase),
		}
		return
	}

	backoffTime := BackoffBase + BackoffCoef*time.Duration(ba.tries*ba.tries)
	if backoffTime > BackoffMax {
		backoffTime = BackoffMax
	}
	ba.until = time.Now().Add(backoffTime)
	ba.tries++
}

// Clear removes a backoff record. Clients should call this after a
// successful Dial.
func (db *DialBackoff) Clear(p peer.ID) {
	db.lock.Lock()
	defer db.lock.Unlock()
	delete(db.entries, p)
}

func (db *DialBackoff) cleanup() {
	db.lock.Lock()
	defer db.lock.Unlock()
	now := time.Now()
	for p, e := range db.entries {
		good := false
		for _, backoff := range e {
			backoffTime := BackoffBase + BackoffCoef*time.Duration(backoff.tries*backoff.tries)
			if backoffTime > BackoffMax {
				backoffTime = BackoffMax
			}
			if now.Before(backoff.until.Add(backoffTime)) {
				good = true
				break
			}
		}
		if !good {
			delete(db.entries, p)
		}
	}
}

// DialPeer connects to a peer.
//
// The idea is that the client of Swarm does not need to know what network
// the connection will happen over. Swarm can use whichever it choses.
// This allows us to use various transport protocols, do NAT traversal/relay,
// etc. to achieve connection.
func (s *Swarm) DialPeer(ctx context.Context, p peer.ID) (network.Conn, error) {
	// Avoid typed nil issues.
	c, err := s.dialPeer(ctx, p)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// internal dial method that returns an unwrapped conn
//
// It is gated by the swarm's dial synchronization systems: dialsync and
// dialbackoff.
func (s *Swarm) dialPeer(ctx context.Context, p peer.ID) (*Conn, error) {
	log.Debugw("dialing peer", "from", s.local, "to", p)
	err := p.Validate()
	if err != nil {
		return nil, err
	}

	if p == s.local {
		return nil, ErrDialToSelf
	}

	// check if we already have an open (usable) connection first, or can't have a usable
	// connection.
	conn, err := s.bestAcceptableConnToPeer(ctx, p)
	if conn != nil || err != nil {
		return conn, err
	}

	if s.gater != nil && !s.gater.InterceptPeerDial(p) {
		log.Debugf("gater disallowed outbound connection to peer %s", p.Pretty())
		return nil, &DialError{Peer: p, Cause: ErrGaterDisallowedConnection}
	}

	// apply the DialPeer timeout
	ctx, cancel := context.WithTimeout(ctx, network.GetDialPeerTimeout(ctx))
	defer cancel()

	conn, err = s.dsync.Dial(ctx, p)
	if err == nil {
		return conn, nil
	}

	log.Debugf("network for %s finished dialing %s", s.local, p)

	if ctx.Err() != nil {
		// Context error trumps any dial errors as it was likely the ultimate cause.
		return nil, ctx.Err()
	}

	if s.ctx.Err() != nil {
		// Ok, so the swarm is shutting down.
		return nil, ErrSwarmClosed
	}

	return nil, err
}

// dialWorkerLoop synchronizes and executes concurrent dials to a single peer
func (s *Swarm) dialWorkerLoop(p peer.ID, reqch <-chan dialRequest) {
	w := newDialWorker(s, p, reqch)
	w.loop()
}

func (s *Swarm) addrsForDial(ctx context.Context, p peer.ID) ([]ma.Multiaddr, error) {
	peerAddrs := s.peers.Addrs(p)
	if len(peerAddrs) == 0 {
		return nil, ErrNoAddresses
	}

	goodAddrs := s.filterKnownUndialables(p, peerAddrs)
	if forceDirect, _ := network.GetForceDirectDial(ctx); forceDirect {
		goodAddrs = ma.FilterAddrs(goodAddrs, s.nonProxyAddr)
	}

	if len(goodAddrs) == 0 {
		return nil, ErrNoGoodAddresses
	}

	return goodAddrs, nil
}

func (s *Swarm) dialNextAddr(ctx context.Context, p peer.ID, addr ma.Multiaddr, resch chan dialResult) error {
	// check the dial backoff
	if forceDirect, _ := network.GetForceDirectDial(ctx); !forceDirect {
		if s.backf.Backoff(p, addr) {
			return ErrDialBackoff
		}
	}

	// start the dial
	s.limitedDial(ctx, p, addr, resch)

	return nil
}

func (s *Swarm) canDial(addr ma.Multiaddr) bool {
	t := s.TransportForDialing(addr)
	return t != nil && t.CanDial(addr)
}

func (s *Swarm) nonProxyAddr(addr ma.Multiaddr) bool {
	t := s.TransportForDialing(addr)
	return !t.Proxy()
}

// filterKnownUndialables takes a list of multiaddrs, and removes those
// that we definitely don't want to dial: addresses configured to be blocked,
// IPv6 link-local addresses, addresses without a dial-capable transport,
// and addresses that we know to be our own.
// This is an optimization to avoid wasting time on dials that we know are going to fail.
func (s *Swarm) filterKnownUndialables(p peer.ID, addrs []ma.Multiaddr) []ma.Multiaddr {
	lisAddrs, _ := s.InterfaceListenAddresses()
	var ourAddrs []ma.Multiaddr
	for _, addr := range lisAddrs {
		protos := addr.Protocols()
		// we're only sure about filtering out /ip4 and /ip6 addresses, so far
		if protos[0].Code == ma.P_IP4 || protos[0].Code == ma.P_IP6 {
			ourAddrs = append(ourAddrs, addr)
		}
	}

	return ma.FilterAddrs(addrs,
		func(addr ma.Multiaddr) bool { return !ma.Contains(ourAddrs, addr) },
		s.canDial,
		// TODO: Consider allowing link-local addresses
		func(addr ma.Multiaddr) bool { return !manet.IsIP6LinkLocal(addr) },
		func(addr ma.Multiaddr) bool {
			return s.gater == nil || s.gater.InterceptAddrDial(p, addr)
		},
	)
}

// limitedDial will start a dial to the given peer when
// it is able, respecting the various different types of rate
// limiting that occur without using extra goroutines per addr
func (s *Swarm) limitedDial(ctx context.Context, p peer.ID, a ma.Multiaddr, resp chan dialResult) {
	timeout := s.dialTimeout
	if lowTimeoutFilters.AddrBlocked(a) && s.dialTimeoutLocal < s.dialTimeout {
		timeout = s.dialTimeoutLocal
	}
	s.limiter.AddDialJob(&dialJob{
		addr:    a,
		peer:    p,
		resp:    resp,
		ctx:     ctx,
		timeout: timeout,
	})
}

// dialAddr is the actual dial for an addr, indirectly invoked through the limiter
func (s *Swarm) dialAddr(ctx context.Context, p peer.ID, addr ma.Multiaddr) (transport.CapableConn, error) {
	// Just to double check. Costs nothing.
	if s.local == p {
		return nil, ErrDialToSelf
	}
	log.Debugf("%s swarm dialing %s %s", s.local, p, addr)

	tpt := s.TransportForDialing(addr)
	if tpt == nil {
		return nil, ErrNoTransport
	}

	connC, err := tpt.Dial(ctx, addr, p)
	if err != nil {
		return nil, err
	}
	canonicallog.LogPeerStatus(100, connC.RemotePeer(), connC.RemoteMultiaddr(), "connection_status", "established", "dir", "outbound")

	// Trust the transport? Yeah... right.
	if connC.RemotePeer() != p {
		connC.Close()
		err = fmt.Errorf("BUG in transport %T: tried to dial %s, dialed %s", p, connC.RemotePeer(), tpt)
		log.Error(err)
		return nil, err
	}

	// success! we got one!
	return connC, nil
}

// TODO We should have a `IsFdConsuming() bool` method on the `Transport` interface in go-libp2p/core/transport.
// This function checks if any of the transport protocols in the address requires a file descriptor.
// For now:
// A Non-circuit address which has the TCP/UNIX protocol is deemed FD consuming.
// For a circuit-relay address, we look at the address of the relay server/proxy
// and use the same logic as above to decide.
func isFdConsumingAddr(addr ma.Multiaddr) bool {
	first, _ := ma.SplitFunc(addr, func(c ma.Component) bool {
		return c.Protocol().Code == ma.P_CIRCUIT
	})

	// for safety
	if first == nil {
		return true
	}

	_, err1 := first.ValueForProtocol(ma.P_TCP)
	_, err2 := first.ValueForProtocol(ma.P_UNIX)
	return err1 == nil || err2 == nil
}

func isExpensiveAddr(addr ma.Multiaddr) bool {
	_, wsErr := addr.ValueForProtocol(ma.P_WS)
	_, wssErr := addr.ValueForProtocol(ma.P_WSS)
	_, wtErr := addr.ValueForProtocol(ma.P_WEBTRANSPORT)
	return wsErr == nil || wssErr == nil || wtErr == nil
}

func isRelayAddr(addr ma.Multiaddr) bool {
	_, err := addr.ValueForProtocol(ma.P_CIRCUIT)
	return err == nil
}
