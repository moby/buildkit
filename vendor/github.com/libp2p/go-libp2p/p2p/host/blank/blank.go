package blankhost

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/libp2p/go-libp2p/core/connmgr"
	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/core/record"
	"github.com/libp2p/go-libp2p/p2p/host/eventbus"

	logging "github.com/ipfs/go-log/v2"

	ma "github.com/multiformats/go-multiaddr"
	mstream "github.com/multiformats/go-multistream"
)

var log = logging.Logger("blankhost")

// BlankHost is the thinnest implementation of the host.Host interface
type BlankHost struct {
	n        network.Network
	mux      *mstream.MultistreamMuxer
	cmgr     connmgr.ConnManager
	eventbus event.Bus
	emitters struct {
		evtLocalProtocolsUpdated event.Emitter
	}
}

type config struct {
	cmgr     connmgr.ConnManager
	eventBus event.Bus
}

type Option = func(cfg *config)

func WithConnectionManager(cmgr connmgr.ConnManager) Option {
	return func(cfg *config) {
		cfg.cmgr = cmgr
	}
}

func WithEventBus(eventBus event.Bus) Option {
	return func(cfg *config) {
		cfg.eventBus = eventBus
	}
}

func NewBlankHost(n network.Network, options ...Option) *BlankHost {
	cfg := config{
		cmgr: &connmgr.NullConnMgr{},
	}
	for _, opt := range options {
		opt(&cfg)
	}

	bh := &BlankHost{
		n:    n,
		cmgr: cfg.cmgr,
		mux:  mstream.NewMultistreamMuxer(),
	}
	if bh.eventbus == nil {
		bh.eventbus = eventbus.NewBus()
	}

	// subscribe the connection manager to network notifications (has no effect with NullConnMgr)
	n.Notify(bh.cmgr.Notifee())

	var err error
	if bh.emitters.evtLocalProtocolsUpdated, err = bh.eventbus.Emitter(&event.EvtLocalProtocolsUpdated{}); err != nil {
		return nil
	}
	evtPeerConnectednessChanged, err := bh.eventbus.Emitter(&event.EvtPeerConnectednessChanged{})
	if err != nil {
		return nil
	}
	n.Notify(newPeerConnectWatcher(evtPeerConnectednessChanged))

	n.SetStreamHandler(bh.newStreamHandler)

	// persist a signed peer record for self to the peerstore.
	if err := bh.initSignedRecord(); err != nil {
		log.Errorf("error creating blank host, err=%s", err)
		return nil
	}

	return bh
}

func (bh *BlankHost) initSignedRecord() error {
	cab, ok := peerstore.GetCertifiedAddrBook(bh.n.Peerstore())
	if !ok {
		log.Error("peerstore does not support signed records")
		return errors.New("peerstore does not support signed records")
	}
	rec := peer.PeerRecordFromAddrInfo(peer.AddrInfo{ID: bh.ID(), Addrs: bh.Addrs()})
	ev, err := record.Seal(rec, bh.Peerstore().PrivKey(bh.ID()))
	if err != nil {
		log.Errorf("failed to create signed record for self, err=%s", err)
		return fmt.Errorf("failed to create signed record for self, err=%s", err)
	}
	_, err = cab.ConsumePeerRecord(ev, peerstore.PermanentAddrTTL)
	if err != nil {
		log.Errorf("failed to persist signed record to peerstore,err=%s", err)
		return fmt.Errorf("failed to persist signed record for self, err=%s", err)
	}
	return err
}

var _ host.Host = (*BlankHost)(nil)

func (bh *BlankHost) Addrs() []ma.Multiaddr {
	addrs, err := bh.n.InterfaceListenAddresses()
	if err != nil {
		log.Debug("error retrieving network interface addrs: ", err)
		return nil
	}

	return addrs
}

func (bh *BlankHost) Close() error {
	return bh.n.Close()
}

func (bh *BlankHost) Connect(ctx context.Context, ai peer.AddrInfo) error {
	// absorb addresses into peerstore
	bh.Peerstore().AddAddrs(ai.ID, ai.Addrs, peerstore.TempAddrTTL)

	cs := bh.n.ConnsToPeer(ai.ID)
	if len(cs) > 0 {
		return nil
	}

	_, err := bh.Network().DialPeer(ctx, ai.ID)
	return err
}

func (bh *BlankHost) Peerstore() peerstore.Peerstore {
	return bh.n.Peerstore()
}

func (bh *BlankHost) ID() peer.ID {
	return bh.n.LocalPeer()
}

func (bh *BlankHost) NewStream(ctx context.Context, p peer.ID, protos ...protocol.ID) (network.Stream, error) {
	s, err := bh.n.NewStream(ctx, p)
	if err != nil {
		return nil, err
	}

	protoStrs := make([]string, len(protos))
	for i, pid := range protos {
		protoStrs[i] = string(pid)
	}

	selected, err := mstream.SelectOneOf(protoStrs, s)
	if err != nil {
		s.Reset()
		return nil, err
	}

	selpid := protocol.ID(selected)
	s.SetProtocol(selpid)
	bh.Peerstore().AddProtocols(p, selected)

	return s, nil
}

func (bh *BlankHost) RemoveStreamHandler(pid protocol.ID) {
	bh.Mux().RemoveHandler(string(pid))
	bh.emitters.evtLocalProtocolsUpdated.Emit(event.EvtLocalProtocolsUpdated{
		Removed: []protocol.ID{pid},
	})
}

func (bh *BlankHost) SetStreamHandler(pid protocol.ID, handler network.StreamHandler) {
	bh.Mux().AddHandler(string(pid), func(p string, rwc io.ReadWriteCloser) error {
		is := rwc.(network.Stream)
		is.SetProtocol(protocol.ID(p))
		handler(is)
		return nil
	})
	bh.emitters.evtLocalProtocolsUpdated.Emit(event.EvtLocalProtocolsUpdated{
		Added: []protocol.ID{pid},
	})
}

func (bh *BlankHost) SetStreamHandlerMatch(pid protocol.ID, m func(string) bool, handler network.StreamHandler) {
	bh.Mux().AddHandlerWithFunc(string(pid), m, func(p string, rwc io.ReadWriteCloser) error {
		is := rwc.(network.Stream)
		is.SetProtocol(protocol.ID(p))
		handler(is)
		return nil
	})
	bh.emitters.evtLocalProtocolsUpdated.Emit(event.EvtLocalProtocolsUpdated{
		Added: []protocol.ID{pid},
	})
}

// newStreamHandler is the remote-opened stream handler for network.Network
func (bh *BlankHost) newStreamHandler(s network.Stream) {
	protoID, handle, err := bh.Mux().Negotiate(s)
	if err != nil {
		log.Infow("protocol negotiation failed", "error", err)
		s.Reset()
		return
	}

	s.SetProtocol(protocol.ID(protoID))

	go handle(protoID, s)
}

// TODO: i'm not sure this really needs to be here
func (bh *BlankHost) Mux() protocol.Switch {
	return bh.mux
}

// TODO: also not sure this fits... Might be better ways around this (leaky abstractions)
func (bh *BlankHost) Network() network.Network {
	return bh.n
}

func (bh *BlankHost) ConnManager() connmgr.ConnManager {
	return bh.cmgr
}

func (bh *BlankHost) EventBus() event.Bus {
	return bh.eventbus
}
