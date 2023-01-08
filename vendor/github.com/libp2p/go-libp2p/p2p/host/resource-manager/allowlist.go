package rcmgr

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"sync"

	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

type Allowlist struct {
	mu sync.RWMutex
	// a simple structure of lists of networks. There is probably a faster way
	// to check if an IP address is in this network than iterating over this
	// list, but this is good enough for small numbers of networks (<1_000).
	// Analyze the benchmark before trying to optimize this.

	// Any peer with these IPs are allowed
	allowedNetworks []*net.IPNet

	// Only the specified peers can use these IPs
	allowedPeerByNetwork map[peer.ID][]*net.IPNet
}

// WithAllowlistedMultiaddrs sets the multiaddrs to be in the allowlist
func WithAllowlistedMultiaddrs(mas []multiaddr.Multiaddr) Option {
	return func(rm *resourceManager) error {
		for _, ma := range mas {
			err := rm.allowlist.Add(ma)
			if err != nil {
				return err
			}
		}
		return nil
	}
}

func newAllowlist() Allowlist {
	return Allowlist{
		allowedPeerByNetwork: make(map[peer.ID][]*net.IPNet),
	}
}

func toIPNet(ma multiaddr.Multiaddr) (*net.IPNet, peer.ID, error) {
	var ipString string
	var mask string
	var allowedPeerStr string
	var allowedPeer peer.ID
	var isIPV4 bool

	multiaddr.ForEach(ma, func(c multiaddr.Component) bool {
		if c.Protocol().Code == multiaddr.P_IP4 || c.Protocol().Code == multiaddr.P_IP6 {
			isIPV4 = c.Protocol().Code == multiaddr.P_IP4
			ipString = c.Value()
		}
		if c.Protocol().Code == multiaddr.P_IPCIDR {
			mask = c.Value()
		}
		if c.Protocol().Code == multiaddr.P_P2P {
			allowedPeerStr = c.Value()
		}
		return ipString == "" || mask == "" || allowedPeerStr == ""
	})

	if ipString == "" {
		return nil, allowedPeer, errors.New("missing ip address")
	}

	if allowedPeerStr != "" {
		var err error
		allowedPeer, err = peer.Decode(allowedPeerStr)
		if err != nil {
			return nil, allowedPeer, fmt.Errorf("failed to decode allowed peer: %w", err)
		}
	}

	if mask == "" {
		ip := net.ParseIP(ipString)
		if ip == nil {
			return nil, allowedPeer, errors.New("invalid ip address")
		}
		var mask net.IPMask
		if isIPV4 {
			mask = net.CIDRMask(32, 32)
		} else {
			mask = net.CIDRMask(128, 128)
		}

		net := &net.IPNet{IP: ip, Mask: mask}
		return net, allowedPeer, nil
	}

	_, ipnet, err := net.ParseCIDR(ipString + "/" + mask)
	return ipnet, allowedPeer, err

}

// Add takes a multiaddr and adds it to the allowlist. The multiaddr should be
// an ip address of the peer with or without a `/p2p` protocol.
// e.g. /ip4/1.2.3.4/p2p/QmFoo, /ip4/1.2.3.4, and /ip4/1.2.3.0/ipcidr/24 are valid.
// /p2p/QmFoo is not valid.
func (al *Allowlist) Add(ma multiaddr.Multiaddr) error {
	ipnet, allowedPeer, err := toIPNet(ma)
	if err != nil {
		return err
	}
	al.mu.Lock()
	defer al.mu.Unlock()

	if allowedPeer != peer.ID("") {
		// We have a peerID constraint
		if al.allowedPeerByNetwork == nil {
			al.allowedPeerByNetwork = make(map[peer.ID][]*net.IPNet)
		}
		al.allowedPeerByNetwork[allowedPeer] = append(al.allowedPeerByNetwork[allowedPeer], ipnet)
	} else {
		al.allowedNetworks = append(al.allowedNetworks, ipnet)
	}
	return nil
}

func (al *Allowlist) Remove(ma multiaddr.Multiaddr) error {
	ipnet, allowedPeer, err := toIPNet(ma)
	if err != nil {
		return err
	}
	al.mu.Lock()
	defer al.mu.Unlock()

	ipNetList := al.allowedNetworks

	if allowedPeer != "" {
		// We have a peerID constraint
		ipNetList = al.allowedPeerByNetwork[allowedPeer]
	}

	if ipNetList == nil {
		return nil
	}

	i := len(ipNetList)
	for i > 0 {
		i--
		if ipNetList[i].IP.Equal(ipnet.IP) && bytes.Equal(ipNetList[i].Mask, ipnet.Mask) {
			// swap remove
			ipNetList[i] = ipNetList[len(ipNetList)-1]
			ipNetList = ipNetList[:len(ipNetList)-1]
			// We only remove one thing
			break
		}
	}

	if allowedPeer != "" {
		al.allowedPeerByNetwork[allowedPeer] = ipNetList
	} else {
		al.allowedNetworks = ipNetList
	}

	return nil
}

func (al *Allowlist) Allowed(ma multiaddr.Multiaddr) bool {
	ip, err := manet.ToIP(ma)
	if err != nil {
		return false
	}
	al.mu.RLock()
	defer al.mu.RUnlock()

	for _, network := range al.allowedNetworks {
		if network.Contains(ip) {
			return true
		}
	}

	for _, allowedNetworks := range al.allowedPeerByNetwork {
		for _, network := range allowedNetworks {
			if network.Contains(ip) {
				return true
			}
		}
	}

	return false
}

func (al *Allowlist) AllowedPeerAndMultiaddr(peerID peer.ID, ma multiaddr.Multiaddr) bool {
	ip, err := manet.ToIP(ma)
	if err != nil {
		return false
	}
	al.mu.RLock()
	defer al.mu.RUnlock()

	for _, network := range al.allowedNetworks {
		if network.Contains(ip) {
			// We found a match that isn't constrained by a peerID
			return true
		}
	}

	if expectedNetworks, ok := al.allowedPeerByNetwork[peerID]; ok {
		for _, expectedNetwork := range expectedNetworks {
			if expectedNetwork.Contains(ip) {
				return true
			}
		}
	}

	return false
}
