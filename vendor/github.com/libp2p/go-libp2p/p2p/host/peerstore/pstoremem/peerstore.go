package pstoremem

import (
	"fmt"
	"io"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	pstore "github.com/libp2p/go-libp2p/p2p/host/peerstore"
)

type pstoremem struct {
	peerstore.Metrics

	*memoryKeyBook
	*memoryAddrBook
	*memoryProtoBook
	*memoryPeerMetadata
}

var _ peerstore.Peerstore = &pstoremem{}

type Option interface{}

// NewPeerstore creates an in-memory threadsafe collection of peers.
// It's the caller's responsibility to call RemovePeer to ensure
// that memory consumption of the peerstore doesn't grow unboundedly.
func NewPeerstore(opts ...Option) (ps *pstoremem, err error) {
	ab := NewAddrBook()
	defer func() {
		if err != nil {
			ab.Close()
		}
	}()

	var protoBookOpts []ProtoBookOption
	for _, opt := range opts {
		switch o := opt.(type) {
		case ProtoBookOption:
			protoBookOpts = append(protoBookOpts, o)
		case AddrBookOption:
			o(ab)
		default:
			return nil, fmt.Errorf("unexpected peer store option: %v", o)
		}
	}
	pb, err := NewProtoBook(protoBookOpts...)
	if err != nil {
		return nil, err
	}
	return &pstoremem{
		Metrics:            pstore.NewMetrics(),
		memoryKeyBook:      NewKeyBook(),
		memoryAddrBook:     ab,
		memoryProtoBook:    pb,
		memoryPeerMetadata: NewPeerMetadata(),
	}, nil
}

func (ps *pstoremem) Close() (err error) {
	var errs []error
	weakClose := func(name string, c interface{}) {
		if cl, ok := c.(io.Closer); ok {
			if err = cl.Close(); err != nil {
				errs = append(errs, fmt.Errorf("%s error: %s", name, err))
			}
		}
	}
	weakClose("keybook", ps.memoryKeyBook)
	weakClose("addressbook", ps.memoryAddrBook)
	weakClose("protobook", ps.memoryProtoBook)
	weakClose("peermetadata", ps.memoryPeerMetadata)

	if len(errs) > 0 {
		return fmt.Errorf("failed while closing peerstore; err(s): %q", errs)
	}
	return nil
}

func (ps *pstoremem) Peers() peer.IDSlice {
	set := map[peer.ID]struct{}{}
	for _, p := range ps.PeersWithKeys() {
		set[p] = struct{}{}
	}
	for _, p := range ps.PeersWithAddrs() {
		set[p] = struct{}{}
	}

	pps := make(peer.IDSlice, 0, len(set))
	for p := range set {
		pps = append(pps, p)
	}
	return pps
}

func (ps *pstoremem) PeerInfo(p peer.ID) peer.AddrInfo {
	return peer.AddrInfo{
		ID:    p,
		Addrs: ps.memoryAddrBook.Addrs(p),
	}
}

// RemovePeer removes entries associated with a peer from:
// * the KeyBook
// * the ProtoBook
// * the PeerMetadata
// * the Metrics
// It DOES NOT remove the peer from the AddrBook.
func (ps *pstoremem) RemovePeer(p peer.ID) {
	ps.memoryKeyBook.RemovePeer(p)
	ps.memoryProtoBook.RemovePeer(p)
	ps.memoryPeerMetadata.RemovePeer(p)
	ps.Metrics.RemovePeer(p)
}
