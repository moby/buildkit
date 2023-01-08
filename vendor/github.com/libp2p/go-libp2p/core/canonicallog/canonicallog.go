package canonicallog

import (
	"fmt"
	"math/rand"
	"net"
	"strings"

	"github.com/libp2p/go-libp2p/core/peer"

	logging "github.com/ipfs/go-log/v2"
	"github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

var log = logging.WithSkip(logging.Logger("canonical-log"), 1)

// LogMisbehavingPeer is the canonical way to log a misbehaving peer.
// Protocols should use this to identify a misbehaving peer to allow the end
// user to easily identify these nodes across protocols and libp2p.
func LogMisbehavingPeer(p peer.ID, peerAddr multiaddr.Multiaddr, component string, err error, msg string) {
	log.Warnf("CANONICAL_MISBEHAVING_PEER: peer=%s addr=%s component=%s err=%q msg=%q", p, peerAddr.String(), component, err, msg)
}

// LogMisbehavingPeerNetAddr is the canonical way to log a misbehaving peer.
// Protocols should use this to identify a misbehaving peer to allow the end
// user to easily identify these nodes across protocols and libp2p.
func LogMisbehavingPeerNetAddr(p peer.ID, peerAddr net.Addr, component string, originalErr error, msg string) {
	ma, err := manet.FromNetAddr(peerAddr)
	if err != nil {
		log.Warnf("CANONICAL_MISBEHAVING_PEER: peer=%s net_addr=%s component=%s err=%q msg=%q", p, peerAddr.String(), component, originalErr, msg)
		return
	}

	LogMisbehavingPeer(p, ma, component, originalErr, msg)
}

// LogPeerStatus logs any useful information about a peer. It takes in a sample
// rate and will only log one in every sampleRate messages (randomly). This is
// useful in surfacing events that are normal in isolation, but may be abnormal
// in large quantities. For example, a successful connection from an IP address
// is normal. 10,000 connections from that same IP address is not normal. libp2p
// itself does nothing besides emitting this log. Hook this up to another tool
// like fail2ban to action on the log.
func LogPeerStatus(sampleRate int, p peer.ID, peerAddr multiaddr.Multiaddr, keyVals ...string) {
	if rand.Intn(sampleRate) == 0 {
		keyValsStr := strings.Builder{}
		for i, kOrV := range keyVals {
			if i%2 == 0 {
				fmt.Fprintf(&keyValsStr, " %v=", kOrV)
			} else {
				fmt.Fprintf(&keyValsStr, "%q", kOrV)
			}
		}
		log.Infof("CANONICAL_PEER_STATUS: peer=%s addr=%s sample_rate=%v%s", p, peerAddr.String(), sampleRate, keyValsStr.String())
	}
}
