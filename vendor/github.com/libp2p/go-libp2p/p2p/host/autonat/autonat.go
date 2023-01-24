package autonat

import (
	"context"
	"errors"
	"math/rand"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/host/eventbus"

	logging "github.com/ipfs/go-log/v2"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

var log = logging.Logger("autonat")

// AmbientAutoNAT is the implementation of ambient NAT autodiscovery
type AmbientAutoNAT struct {
	host host.Host

	*config

	ctx               context.Context
	ctxCancel         context.CancelFunc // is closed when Close is called
	backgroundRunning chan struct{}      // is closed when the background go routine exits

	inboundConn  chan network.Conn
	observations chan autoNATResult
	// status is an autoNATResult reflecting current status.
	status atomic.Value
	// Reflects the confidence on of the NATStatus being private, as a single
	// dialback may fail for reasons unrelated to NAT.
	// If it is <3, then multiple autoNAT peers may be contacted for dialback
	// If only a single autoNAT peer is known, then the confidence increases
	// for each failure until it reaches 3.
	confidence   int
	lastInbound  time.Time
	lastProbeTry time.Time
	lastProbe    time.Time
	recentProbes map[peer.ID]time.Time

	service *autoNATService

	emitReachabilityChanged event.Emitter
	subscriber              event.Subscription
}

// StaticAutoNAT is a simple AutoNAT implementation when a single NAT status is desired.
type StaticAutoNAT struct {
	host         host.Host
	reachability network.Reachability
	service      *autoNATService
}

type autoNATResult struct {
	network.Reachability
	address ma.Multiaddr
}

// New creates a new NAT autodiscovery system attached to a host
func New(h host.Host, options ...Option) (AutoNAT, error) {
	var err error
	conf := new(config)
	conf.host = h
	conf.dialPolicy.host = h

	if err = defaults(conf); err != nil {
		return nil, err
	}
	if conf.addressFunc == nil {
		conf.addressFunc = h.Addrs
	}

	for _, o := range options {
		if err = o(conf); err != nil {
			return nil, err
		}
	}
	emitReachabilityChanged, _ := h.EventBus().Emitter(new(event.EvtLocalReachabilityChanged), eventbus.Stateful)

	var service *autoNATService
	if (!conf.forceReachability || conf.reachability == network.ReachabilityPublic) && conf.dialer != nil {
		service, err = newAutoNATService(conf)
		if err != nil {
			return nil, err
		}
		service.Enable()
	}

	if conf.forceReachability {
		emitReachabilityChanged.Emit(event.EvtLocalReachabilityChanged{Reachability: conf.reachability})

		return &StaticAutoNAT{
			host:         h,
			reachability: conf.reachability,
			service:      service,
		}, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	as := &AmbientAutoNAT{
		ctx:               ctx,
		ctxCancel:         cancel,
		backgroundRunning: make(chan struct{}),
		host:              h,
		config:            conf,
		inboundConn:       make(chan network.Conn, 5),
		observations:      make(chan autoNATResult, 1),

		emitReachabilityChanged: emitReachabilityChanged,
		service:                 service,
		recentProbes:            make(map[peer.ID]time.Time),
	}
	as.status.Store(autoNATResult{network.ReachabilityUnknown, nil})

	subscriber, err := as.host.EventBus().Subscribe([]interface{}{new(event.EvtLocalAddressesUpdated), new(event.EvtPeerIdentificationCompleted)})
	if err != nil {
		return nil, err
	}
	as.subscriber = subscriber

	h.Network().Notify(as)
	go as.background()

	return as, nil
}

// Status returns the AutoNAT observed reachability status.
func (as *AmbientAutoNAT) Status() network.Reachability {
	s := as.status.Load().(autoNATResult)
	return s.Reachability
}

func (as *AmbientAutoNAT) emitStatus() {
	status := as.status.Load().(autoNATResult)
	as.emitReachabilityChanged.Emit(event.EvtLocalReachabilityChanged{Reachability: status.Reachability})
}

// PublicAddr returns the publicly connectable Multiaddr of this node if one is known.
func (as *AmbientAutoNAT) PublicAddr() (ma.Multiaddr, error) {
	s := as.status.Load().(autoNATResult)
	if s.Reachability != network.ReachabilityPublic {
		return nil, errors.New("NAT status is not public")
	}

	return s.address, nil
}

func ipInList(candidate ma.Multiaddr, list []ma.Multiaddr) bool {
	candidateIP, _ := manet.ToIP(candidate)
	for _, i := range list {
		if ip, err := manet.ToIP(i); err == nil && ip.Equal(candidateIP) {
			return true
		}
	}
	return false
}

func (as *AmbientAutoNAT) background() {
	defer close(as.backgroundRunning)
	// wait a bit for the node to come online and establish some connections
	// before starting autodetection
	delay := as.config.bootDelay

	var lastAddrUpdated time.Time
	subChan := as.subscriber.Out()
	defer as.subscriber.Close()
	defer as.emitReachabilityChanged.Close()

	timer := time.NewTimer(delay)
	defer timer.Stop()
	timerRunning := true
	for {
		select {
		// new inbound connection.
		case conn := <-as.inboundConn:
			localAddrs := as.host.Addrs()
			ca := as.status.Load().(autoNATResult)
			if ca.address != nil {
				localAddrs = append(localAddrs, ca.address)
			}
			if manet.IsPublicAddr(conn.RemoteMultiaddr()) &&
				!ipInList(conn.RemoteMultiaddr(), localAddrs) {
				as.lastInbound = time.Now()
			}

		case e := <-subChan:
			switch e := e.(type) {
			case event.EvtLocalAddressesUpdated:
				if !lastAddrUpdated.Add(time.Second).After(time.Now()) {
					lastAddrUpdated = time.Now()
					if as.confidence > 1 {
						as.confidence--
					}
				}
			case event.EvtPeerIdentificationCompleted:
				if s, err := as.host.Peerstore().SupportsProtocols(e.Peer, AutoNATProto); err == nil && len(s) > 0 {
					currentStatus := as.status.Load().(autoNATResult)
					if currentStatus.Reachability == network.ReachabilityUnknown {
						as.tryProbe(e.Peer)
					}
				}
			default:
				log.Errorf("unknown event type: %T", e)
			}

		// probe finished.
		case result, ok := <-as.observations:
			if !ok {
				return
			}
			as.recordObservation(result)
		case <-timer.C:
			peer := as.getPeerToProbe()
			as.tryProbe(peer)
			timerRunning = false
		case <-as.ctx.Done():
			return
		}

		// Drain the timer channel if it hasn't fired in preparation for Resetting it.
		if timerRunning && !timer.Stop() {
			<-timer.C
		}
		timer.Reset(as.scheduleProbe())
		timerRunning = true
	}
}

func (as *AmbientAutoNAT) cleanupRecentProbes() {
	fixedNow := time.Now()
	for k, v := range as.recentProbes {
		if fixedNow.Sub(v) > as.throttlePeerPeriod {
			delete(as.recentProbes, k)
		}
	}
}

// scheduleProbe calculates when the next probe should be scheduled for.
func (as *AmbientAutoNAT) scheduleProbe() time.Duration {
	// Our baseline is a probe every 'AutoNATRefreshInterval'
	// This is modulated by:
	// * if we are in an unknown state, or have low confidence, that should drop to 'AutoNATRetryInterval'
	// * recent inbound connections (implying continued connectivity) should decrease the retry when public
	// * recent inbound connections when not public mean we should try more actively to see if we're public.
	fixedNow := time.Now()
	currentStatus := as.status.Load().(autoNATResult)

	nextProbe := fixedNow
	// Don't look for peers in the peer store more than once per second.
	if !as.lastProbeTry.IsZero() {
		backoff := as.lastProbeTry.Add(time.Second)
		if backoff.After(nextProbe) {
			nextProbe = backoff
		}
	}
	if !as.lastProbe.IsZero() {
		untilNext := as.config.refreshInterval
		if currentStatus.Reachability == network.ReachabilityUnknown {
			untilNext = as.config.retryInterval
		} else if as.confidence < 3 {
			untilNext = as.config.retryInterval
		} else if currentStatus.Reachability == network.ReachabilityPublic && as.lastInbound.After(as.lastProbe) {
			untilNext *= 2
		} else if currentStatus.Reachability != network.ReachabilityPublic && as.lastInbound.After(as.lastProbe) {
			untilNext /= 5
		}

		if as.lastProbe.Add(untilNext).After(nextProbe) {
			nextProbe = as.lastProbe.Add(untilNext)
		}
	}

	return nextProbe.Sub(fixedNow)
}

// Update the current status based on an observed result.
func (as *AmbientAutoNAT) recordObservation(observation autoNATResult) {
	currentStatus := as.status.Load().(autoNATResult)
	if observation.Reachability == network.ReachabilityPublic {
		log.Debugf("NAT status is public")
		changed := false
		if currentStatus.Reachability != network.ReachabilityPublic {
			// we are flipping our NATStatus, so confidence drops to 0
			as.confidence = 0
			if as.service != nil {
				as.service.Enable()
			}
			changed = true
		} else if as.confidence < 3 {
			as.confidence++
		}
		if observation.address != nil {
			if !changed && currentStatus.address != nil && !observation.address.Equal(currentStatus.address) {
				as.confidence--
			}
			if currentStatus.address == nil || !observation.address.Equal(currentStatus.address) {
				changed = true
			}
			as.status.Store(observation)
		}
		if observation.address != nil && changed {
			as.emitStatus()
		}
	} else if observation.Reachability == network.ReachabilityPrivate {
		log.Debugf("NAT status is private")
		if currentStatus.Reachability == network.ReachabilityPublic {
			if as.confidence > 0 {
				as.confidence--
			} else {
				// we are flipping our NATStatus, so confidence drops to 0
				as.confidence = 0
				as.status.Store(observation)
				if as.service != nil {
					as.service.Disable()
				}
				as.emitStatus()
			}
		} else if as.confidence < 3 {
			as.confidence++
			as.status.Store(observation)
			if currentStatus.Reachability != network.ReachabilityPrivate {
				as.emitStatus()
			}
		}
	} else if as.confidence > 0 {
		// don't just flip to unknown, reduce confidence first
		as.confidence--
	} else {
		log.Debugf("NAT status is unknown")
		as.status.Store(autoNATResult{network.ReachabilityUnknown, nil})
		if currentStatus.Reachability != network.ReachabilityUnknown {
			if as.service != nil {
				as.service.Enable()
			}
			as.emitStatus()
		}
	}
}

func (as *AmbientAutoNAT) tryProbe(p peer.ID) bool {
	as.lastProbeTry = time.Now()
	if p.Validate() != nil {
		return false
	}

	if lastTime, ok := as.recentProbes[p]; ok {
		if time.Since(lastTime) < as.throttlePeerPeriod {
			return false
		}
	}
	as.cleanupRecentProbes()

	info := as.host.Peerstore().PeerInfo(p)

	if !as.config.dialPolicy.skipPeer(info.Addrs) {
		as.recentProbes[p] = time.Now()
		as.lastProbe = time.Now()
		go as.probe(&info)
		return true
	}
	return false
}

func (as *AmbientAutoNAT) probe(pi *peer.AddrInfo) {
	cli := NewAutoNATClient(as.host, as.config.addressFunc)
	ctx, cancel := context.WithTimeout(as.ctx, as.config.requestTimeout)
	defer cancel()

	a, err := cli.DialBack(ctx, pi.ID)

	var result autoNATResult
	switch {
	case err == nil:
		log.Debugf("Dialback through %s successful; public address is %s", pi.ID.Pretty(), a.String())
		result.Reachability = network.ReachabilityPublic
		result.address = a
	case IsDialError(err):
		log.Debugf("Dialback through %s failed", pi.ID.Pretty())
		result.Reachability = network.ReachabilityPrivate
	default:
		result.Reachability = network.ReachabilityUnknown
	}

	select {
	case as.observations <- result:
	case <-as.ctx.Done():
		return
	}
}

func (as *AmbientAutoNAT) getPeerToProbe() peer.ID {
	peers := as.host.Network().Peers()
	if len(peers) == 0 {
		return ""
	}

	candidates := make([]peer.ID, 0, len(peers))

	for _, p := range peers {
		info := as.host.Peerstore().PeerInfo(p)
		// Exclude peers which don't support the autonat protocol.
		if proto, err := as.host.Peerstore().SupportsProtocols(p, AutoNATProto); len(proto) == 0 || err != nil {
			continue
		}

		// Exclude peers in backoff.
		if lastTime, ok := as.recentProbes[p]; ok {
			if time.Since(lastTime) < as.throttlePeerPeriod {
				continue
			}
		}

		if as.config.dialPolicy.skipPeer(info.Addrs) {
			continue
		}
		candidates = append(candidates, p)
	}

	if len(candidates) == 0 {
		return ""
	}

	shufflePeers(candidates)
	return candidates[0]
}

func (as *AmbientAutoNAT) Close() error {
	as.ctxCancel()
	if as.service != nil {
		as.service.Disable()
	}
	<-as.backgroundRunning
	return nil
}

func shufflePeers(peers []peer.ID) {
	for i := range peers {
		j := rand.Intn(i + 1)
		peers[i], peers[j] = peers[j], peers[i]
	}
}

// Status returns the AutoNAT observed reachability status.
func (s *StaticAutoNAT) Status() network.Reachability {
	return s.reachability
}

// PublicAddr returns the publicly connectable Multiaddr of this node if one is known.
func (s *StaticAutoNAT) PublicAddr() (ma.Multiaddr, error) {
	if s.reachability != network.ReachabilityPublic {
		return nil, errors.New("NAT status is not public")
	}
	return nil, errors.New("no available address")
}

func (s *StaticAutoNAT) Close() error {
	if s.service != nil {
		s.service.Disable()
	}
	return nil
}
