package api

import (
	peer "github.com/libp2p/go-libp2p/core/peer"
)

// PeersToStrings Encodes a list of peers.
func PeersToStrings(peers []peer.ID) []string {
	strs := make([]string, len(peers))
	for i, p := range peers {
		if p != "" {
			strs[i] = p.String()
		}
	}
	return strs
}

// StringsToPeers decodes peer.IDs from strings.
func StringsToPeers(strs []string) []peer.ID {
	peers := []peer.ID{}
	for _, p := range strs {
		pid, err := peer.Decode(p)
		if err != nil {
			continue
		}
		peers = append(peers, pid)
	}
	return peers
}
