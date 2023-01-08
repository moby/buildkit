package autonat

import (
	"net"

	"github.com/libp2p/go-libp2p/core/host"

	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

type dialPolicy struct {
	allowSelfDials bool
	host           host.Host
}

// skipDial indicates that a multiaddress isn't worth attempted dialing.
// The same logic is used when the autonat client is considering if
// a remote peer is worth using as a server, and when the server is
// considering if a requested client is worth dialing back.
func (d *dialPolicy) skipDial(addr ma.Multiaddr) bool {
	// skip relay addresses
	_, err := addr.ValueForProtocol(ma.P_CIRCUIT)
	if err == nil {
		return true
	}

	if d.allowSelfDials {
		return false
	}

	// skip private network (unroutable) addresses
	if !manet.IsPublicAddr(addr) {
		return true
	}
	candidateIP, err := manet.ToIP(addr)
	if err != nil {
		return true
	}

	// Skip dialing addresses we believe are the local node's
	for _, localAddr := range d.host.Addrs() {
		localIP, err := manet.ToIP(localAddr)
		if err != nil {
			continue
		}
		if localIP.Equal(candidateIP) {
			return true
		}
	}

	return false
}

// skipPeer indicates that the collection of multiaddresses representing a peer
// isn't worth attempted dialing. If one of the addresses matches an address
// we believe is ours, we exclude the peer, even if there are other valid
// public addresses in the list.
func (d *dialPolicy) skipPeer(addrs []ma.Multiaddr) bool {
	localAddrs := d.host.Addrs()
	localHosts := make([]net.IP, 0)
	for _, lAddr := range localAddrs {
		if _, err := lAddr.ValueForProtocol(ma.P_CIRCUIT); err != nil && manet.IsPublicAddr(lAddr) {
			lIP, err := manet.ToIP(lAddr)
			if err != nil {
				continue
			}
			localHosts = append(localHosts, lIP)
		}
	}

	// if a public IP of the peer is one of ours: skip the peer.
	goodPublic := false
	for _, addr := range addrs {
		if _, err := addr.ValueForProtocol(ma.P_CIRCUIT); err != nil && manet.IsPublicAddr(addr) {
			aIP, err := manet.ToIP(addr)
			if err != nil {
				continue
			}

			for _, lIP := range localHosts {
				if lIP.Equal(aIP) {
					return true
				}
			}
			goodPublic = true
		}
	}

	if d.allowSelfDials {
		return false
	}

	return !goodPublic
}
