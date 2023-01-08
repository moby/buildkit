package rcmgr

import (
	"bytes"
	"sort"
	"strings"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

// ResourceScopeLimiter is a trait interface that allows you to access scope limits.
type ResourceScopeLimiter interface {
	Limit() Limit
	SetLimit(Limit)
}

var _ ResourceScopeLimiter = (*resourceScope)(nil)

// ResourceManagerStat is a trait that allows you to access resource manager state.
type ResourceManagerState interface {
	ListServices() []string
	ListProtocols() []protocol.ID
	ListPeers() []peer.ID

	Stat() ResourceManagerStat
}

type ResourceManagerStat struct {
	System    network.ScopeStat
	Transient network.ScopeStat
	Services  map[string]network.ScopeStat
	Protocols map[protocol.ID]network.ScopeStat
	Peers     map[peer.ID]network.ScopeStat
}

var _ ResourceManagerState = (*resourceManager)(nil)

func (s *resourceScope) Limit() Limit {
	s.Lock()
	defer s.Unlock()

	return s.rc.limit
}

func (s *resourceScope) SetLimit(limit Limit) {
	s.Lock()
	defer s.Unlock()

	s.rc.limit = limit
}

func (s *protocolScope) SetLimit(limit Limit) {
	s.rcmgr.setStickyProtocol(s.proto)
	s.resourceScope.SetLimit(limit)
}

func (s *peerScope) SetLimit(limit Limit) {
	s.rcmgr.setStickyPeer(s.peer)
	s.resourceScope.SetLimit(limit)
}

func (r *resourceManager) ListServices() []string {
	r.mx.Lock()
	defer r.mx.Unlock()

	result := make([]string, 0, len(r.svc))
	for svc := range r.svc {
		result = append(result, svc)
	}

	sort.Slice(result, func(i, j int) bool {
		return strings.Compare(result[i], result[j]) < 0
	})

	return result
}

func (r *resourceManager) ListProtocols() []protocol.ID {
	r.mx.Lock()
	defer r.mx.Unlock()

	result := make([]protocol.ID, 0, len(r.proto))
	for p := range r.proto {
		result = append(result, p)
	}

	sort.Slice(result, func(i, j int) bool {
		return strings.Compare(string(result[i]), string(result[j])) < 0
	})

	return result
}

func (r *resourceManager) ListPeers() []peer.ID {
	r.mx.Lock()
	defer r.mx.Unlock()

	result := make([]peer.ID, 0, len(r.peer))
	for p := range r.peer {
		result = append(result, p)
	}

	sort.Slice(result, func(i, j int) bool {
		return bytes.Compare([]byte(result[i]), []byte(result[j])) < 0
	})

	return result
}

func (r *resourceManager) Stat() (result ResourceManagerStat) {
	r.mx.Lock()
	svcs := make([]*serviceScope, 0, len(r.svc))
	for _, svc := range r.svc {
		svcs = append(svcs, svc)
	}
	protos := make([]*protocolScope, 0, len(r.proto))
	for _, proto := range r.proto {
		protos = append(protos, proto)
	}
	peers := make([]*peerScope, 0, len(r.peer))
	for _, peer := range r.peer {
		peers = append(peers, peer)
	}
	r.mx.Unlock()

	// Note: there is no global lock, so the system is updating while we are dumping its state...
	//       as such stats might not exactly add up to the system level; we take the system stat
	//       last nonetheless so that this is the most up-to-date snapshot
	result.Peers = make(map[peer.ID]network.ScopeStat, len(peers))
	for _, peer := range peers {
		result.Peers[peer.peer] = peer.Stat()
	}
	result.Protocols = make(map[protocol.ID]network.ScopeStat, len(protos))
	for _, proto := range protos {
		result.Protocols[proto.proto] = proto.Stat()
	}
	result.Services = make(map[string]network.ScopeStat, len(svcs))
	for _, svc := range svcs {
		result.Services[svc.service] = svc.Stat()
	}
	result.Transient = r.transient.Stat()
	result.System = r.system.Stat()

	return result
}
