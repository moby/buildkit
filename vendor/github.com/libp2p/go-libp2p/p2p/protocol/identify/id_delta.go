package identify

import (
	"time"

	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	pb "github.com/libp2p/go-libp2p/p2p/protocol/identify/pb"

	"github.com/libp2p/go-msgio/protoio"
)

const IDDelta = "/p2p/id/delta/1.0.0"

const deltaMsgSize = 2048

// deltaHandler handles incoming delta updates from peers.
func (ids *idService) deltaHandler(s network.Stream) {
	if err := s.Scope().SetService(ServiceName); err != nil {
		log.Warnf("error attaching stream to identify service: %s", err)
		s.Reset()
		return
	}

	if err := s.Scope().ReserveMemory(deltaMsgSize, network.ReservationPriorityAlways); err != nil {
		log.Warnf("error reserving memory for identify stream: %s", err)
		s.Reset()
		return
	}
	defer s.Scope().ReleaseMemory(deltaMsgSize)

	_ = s.SetReadDeadline(time.Now().Add(StreamReadTimeout))

	c := s.Conn()

	r := protoio.NewDelimitedReader(s, deltaMsgSize)
	mes := pb.Identify{}
	if err := r.ReadMsg(&mes); err != nil {
		log.Warn("error reading identify message: ", err)
		_ = s.Reset()
		return
	}

	defer s.Close()

	log.Debugf("%s received message from %s %s", s.Protocol(), c.RemotePeer(), c.RemoteMultiaddr())

	delta := mes.GetDelta()
	if delta == nil {
		return
	}

	p := s.Conn().RemotePeer()
	if err := ids.consumeDelta(p, delta); err != nil {
		_ = s.Reset()
		log.Warnf("delta update from peer %s failed: %s", p, err)
	}
}

// consumeDelta processes an incoming delta from a peer, updating the peerstore
// and emitting the appropriate events.
func (ids *idService) consumeDelta(id peer.ID, delta *pb.Delta) error {
	err := ids.Host.Peerstore().AddProtocols(id, delta.GetAddedProtocols()...)
	if err != nil {
		return err
	}

	err = ids.Host.Peerstore().RemoveProtocols(id, delta.GetRmProtocols()...)
	if err != nil {
		return err
	}

	evt := event.EvtPeerProtocolsUpdated{
		Peer:    id,
		Added:   protocol.ConvertFromStrings(delta.GetAddedProtocols()),
		Removed: protocol.ConvertFromStrings(delta.GetRmProtocols()),
	}
	ids.emitters.evtPeerProtocolsUpdated.Emit(evt)
	return nil
}
