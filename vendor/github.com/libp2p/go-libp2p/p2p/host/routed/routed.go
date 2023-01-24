package routedhost

import (
	"context"
	"fmt"
	"time"

	"github.com/libp2p/go-libp2p/core/connmgr"
	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/core/protocol"

	logging "github.com/ipfs/go-log/v2"

	ma "github.com/multiformats/go-multiaddr"
)

var log = logging.Logger("routedhost")

// AddressTTL is the expiry time for our addresses.
// We expire them quickly.
const AddressTTL = time.Second * 10

// RoutedHost is a p2p Host that includes a routing system.
// This allows the Host to find the addresses for peers when
// it does not have them.
type RoutedHost struct {
	host  host.Host // embedded other host.
	route Routing
}

type Routing interface {
	FindPeer(context.Context, peer.ID) (peer.AddrInfo, error)
}

func Wrap(h host.Host, r Routing) *RoutedHost {
	return &RoutedHost{h, r}
}

// Connect ensures there is a connection between this host and the peer with
// given peer.ID. See (host.Host).Connect for more information.
//
// RoutedHost's Connect differs in that if the host has no addresses for a
// given peer, it will use its routing system to try to find some.
func (rh *RoutedHost) Connect(ctx context.Context, pi peer.AddrInfo) error {
	// first, check if we're already connected unless force direct dial.
	forceDirect, _ := network.GetForceDirectDial(ctx)
	if !forceDirect {
		if rh.Network().Connectedness(pi.ID) == network.Connected {
			return nil
		}
	}

	// if we were given some addresses, keep + use them.
	if len(pi.Addrs) > 0 {
		rh.Peerstore().AddAddrs(pi.ID, pi.Addrs, peerstore.TempAddrTTL)
	}

	// Check if we have some addresses in our recent memory.
	addrs := rh.Peerstore().Addrs(pi.ID)
	if len(addrs) < 1 {
		// no addrs? find some with the routing system.
		var err error
		addrs, err = rh.findPeerAddrs(ctx, pi.ID)
		if err != nil {
			return err
		}
	}

	// Issue 448: if our address set includes routed specific relay addrs,
	// we need to make sure the relay's addr itself is in the peerstore or else
	// we won't be able to dial it.
	for _, addr := range addrs {
		if _, err := addr.ValueForProtocol(ma.P_CIRCUIT); err != nil {
			// not a relay address
			continue
		}

		if addr.Protocols()[0].Code != ma.P_P2P {
			// not a routed relay specific address
			continue
		}

		relay, _ := addr.ValueForProtocol(ma.P_P2P)
		relayID, err := peer.Decode(relay)
		if err != nil {
			log.Debugf("failed to parse relay ID in address %s: %s", relay, err)
			continue
		}

		if len(rh.Peerstore().Addrs(relayID)) > 0 {
			// we already have addrs for this relay
			continue
		}

		relayAddrs, err := rh.findPeerAddrs(ctx, relayID)
		if err != nil {
			log.Debugf("failed to find relay %s: %s", relay, err)
			continue
		}

		rh.Peerstore().AddAddrs(relayID, relayAddrs, peerstore.TempAddrTTL)
	}

	// if we're here, we got some addrs. let's use our wrapped host to connect.
	pi.Addrs = addrs
	return rh.host.Connect(ctx, pi)
}

func (rh *RoutedHost) findPeerAddrs(ctx context.Context, id peer.ID) ([]ma.Multiaddr, error) {
	pi, err := rh.route.FindPeer(ctx, id)
	if err != nil {
		return nil, err // couldnt find any :(
	}

	if pi.ID != id {
		err = fmt.Errorf("routing failure: provided addrs for different peer")
		log.Errorw("got wrong peer",
			"error", err,
			"wantedPeer", id,
			"gotPeer", pi.ID,
		)
		return nil, err
	}

	return pi.Addrs, nil
}

func (rh *RoutedHost) ID() peer.ID {
	return rh.host.ID()
}

func (rh *RoutedHost) Peerstore() peerstore.Peerstore {
	return rh.host.Peerstore()
}

func (rh *RoutedHost) Addrs() []ma.Multiaddr {
	return rh.host.Addrs()
}

func (rh *RoutedHost) Network() network.Network {
	return rh.host.Network()
}

func (rh *RoutedHost) Mux() protocol.Switch {
	return rh.host.Mux()
}

func (rh *RoutedHost) EventBus() event.Bus {
	return rh.host.EventBus()
}

func (rh *RoutedHost) SetStreamHandler(pid protocol.ID, handler network.StreamHandler) {
	rh.host.SetStreamHandler(pid, handler)
}

func (rh *RoutedHost) SetStreamHandlerMatch(pid protocol.ID, m func(string) bool, handler network.StreamHandler) {
	rh.host.SetStreamHandlerMatch(pid, m, handler)
}

func (rh *RoutedHost) RemoveStreamHandler(pid protocol.ID) {
	rh.host.RemoveStreamHandler(pid)
}

func (rh *RoutedHost) NewStream(ctx context.Context, p peer.ID, pids ...protocol.ID) (network.Stream, error) {
	// Ensure we have a connection, with peer addresses resolved by the routing system (#207)
	// It is not sufficient to let the underlying host connect, it will most likely not have
	// any addresses for the peer without any prior connections.
	// If the caller wants to prevent the host from dialing, it should use the NoDial option.
	if nodial, _ := network.GetNoDial(ctx); !nodial {
		err := rh.Connect(ctx, peer.AddrInfo{ID: p})
		if err != nil {
			return nil, err
		}
	}

	return rh.host.NewStream(ctx, p, pids...)
}
func (rh *RoutedHost) Close() error {
	// no need to close IpfsRouting. we dont own it.
	return rh.host.Close()
}
func (rh *RoutedHost) ConnManager() connmgr.ConnManager {
	return rh.host.ConnManager()
}

var _ (host.Host) = (*RoutedHost)(nil)
