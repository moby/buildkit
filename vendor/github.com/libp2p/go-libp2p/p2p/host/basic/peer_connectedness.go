package basichost

import (
	"sync"

	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	ma "github.com/multiformats/go-multiaddr"
)

type peerConnectWatcher struct {
	emitter event.Emitter

	mutex     sync.Mutex
	connected map[peer.ID]struct{}
}

var _ network.Notifiee = &peerConnectWatcher{}

func newPeerConnectWatcher(emitter event.Emitter) *peerConnectWatcher {
	return &peerConnectWatcher{
		emitter:   emitter,
		connected: make(map[peer.ID]struct{}),
	}
}

func (w *peerConnectWatcher) Listen(network.Network, ma.Multiaddr)      {}
func (w *peerConnectWatcher) ListenClose(network.Network, ma.Multiaddr) {}

func (w *peerConnectWatcher) Connected(n network.Network, conn network.Conn) {
	p := conn.RemotePeer()
	w.handleTransition(p, n.Connectedness(p))
}

func (w *peerConnectWatcher) Disconnected(n network.Network, conn network.Conn) {
	p := conn.RemotePeer()
	w.handleTransition(p, n.Connectedness(p))
}

func (w *peerConnectWatcher) handleTransition(p peer.ID, state network.Connectedness) {
	if changed := w.checkTransition(p, state); !changed {
		return
	}
	w.emitter.Emit(event.EvtPeerConnectednessChanged{
		Peer:          p,
		Connectedness: state,
	})
}

func (w *peerConnectWatcher) checkTransition(p peer.ID, state network.Connectedness) bool {
	w.mutex.Lock()
	defer w.mutex.Unlock()
	switch state {
	case network.Connected:
		if _, ok := w.connected[p]; ok {
			return false
		}
		w.connected[p] = struct{}{}
		return true
	case network.NotConnected:
		if _, ok := w.connected[p]; ok {
			delete(w.connected, p)
			return true
		}
		return false
	default:
		return false
	}
}
