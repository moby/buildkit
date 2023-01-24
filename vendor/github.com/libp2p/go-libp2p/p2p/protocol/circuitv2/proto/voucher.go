package proto

import (
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/record"
	pbv2 "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/pb"
)

const RecordDomain = "libp2p-relay-rsvp"

// TODO: register in multicodec table in https://github.com/multiformats/multicodec
var RecordCodec = []byte{0x03, 0x02}

func init() {
	record.RegisterType(&ReservationVoucher{})
}

type ReservationVoucher struct {
	// Relay is the ID of the peer providing relay service
	Relay peer.ID
	// Peer is the ID of the peer receiving relay service through Relay
	Peer peer.ID
	// Expiration is the expiration time of the reservation
	Expiration time.Time
}

var _ record.Record = (*ReservationVoucher)(nil)

func (rv *ReservationVoucher) Domain() string {
	return RecordDomain
}

func (rv *ReservationVoucher) Codec() []byte {
	return RecordCodec
}

func (rv *ReservationVoucher) MarshalRecord() ([]byte, error) {
	relay := []byte(rv.Relay)
	peer := []byte(rv.Peer)
	expiration := uint64(rv.Expiration.Unix())
	pbrv := &pbv2.ReservationVoucher{
		Relay:      relay,
		Peer:       peer,
		Expiration: &expiration,
	}

	return pbrv.Marshal()
}

func (rv *ReservationVoucher) UnmarshalRecord(blob []byte) error {
	pbrv := pbv2.ReservationVoucher{}
	err := pbrv.Unmarshal(blob)
	if err != nil {
		return err
	}

	rv.Relay, err = peer.IDFromBytes(pbrv.GetRelay())
	if err != nil {
		return err
	}

	rv.Peer, err = peer.IDFromBytes(pbrv.GetPeer())
	if err != nil {
		return err
	}

	rv.Expiration = time.Unix(int64(pbrv.GetExpiration()), 0)
	return nil
}
