package client

import (
	"context"
	"fmt"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/core/record"
	pbv2 "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/pb"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/proto"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/util"

	ma "github.com/multiformats/go-multiaddr"
)

var ReserveTimeout = time.Minute

// Reservation is a struct carrying information about a relay/v2 slot reservation.
type Reservation struct {
	// Expiration is the expiration time of the reservation
	Expiration time.Time
	// Addrs contains the vouched public addresses of the reserving peer, which can be
	// announced to the network
	Addrs []ma.Multiaddr

	// LimitDuration is the time limit for which the relay will keep a relayed connection
	// open. If 0, there is no limit.
	LimitDuration time.Duration
	// LimitData is the number of bytes that the relay will relay in each direction before
	// resetting a relayed connection.
	LimitData uint64

	// Voucher is a signed reservation voucher provided by the relay
	Voucher *proto.ReservationVoucher
}

// Reserve reserves a slot in a relay and returns the reservation information.
// Clients must reserve slots in order for the relay to relay connections to them.
func Reserve(ctx context.Context, h host.Host, ai peer.AddrInfo) (*Reservation, error) {
	if len(ai.Addrs) > 0 {
		h.Peerstore().AddAddrs(ai.ID, ai.Addrs, peerstore.TempAddrTTL)
	}

	s, err := h.NewStream(ctx, ai.ID, proto.ProtoIDv2Hop)
	if err != nil {
		return nil, err
	}
	defer s.Close()

	rd := util.NewDelimitedReader(s, maxMessageSize)
	wr := util.NewDelimitedWriter(s)
	defer rd.Close()

	var msg pbv2.HopMessage
	msg.Type = pbv2.HopMessage_RESERVE.Enum()

	s.SetDeadline(time.Now().Add(ReserveTimeout))

	if err := wr.WriteMsg(&msg); err != nil {
		s.Reset()
		return nil, fmt.Errorf("error writing reservation message: %w", err)
	}

	msg.Reset()

	if err := rd.ReadMsg(&msg); err != nil {
		s.Reset()
		return nil, fmt.Errorf("error reading reservation response message: %w", err)
	}

	if msg.GetType() != pbv2.HopMessage_STATUS {
		return nil, fmt.Errorf("unexpected relay response: not a status message (%d)", msg.GetType())
	}

	if status := msg.GetStatus(); status != pbv2.Status_OK {
		return nil, fmt.Errorf("reservation failed: %s (%d)", pbv2.Status_name[int32(status)], status)
	}

	rsvp := msg.GetReservation()
	if rsvp == nil {
		return nil, fmt.Errorf("missing reservation info")
	}

	result := &Reservation{}
	result.Expiration = time.Unix(int64(rsvp.GetExpire()), 0)
	if result.Expiration.Before(time.Now()) {
		return nil, fmt.Errorf("received reservation with expiration date in the past: %s", result.Expiration)
	}

	addrs := rsvp.GetAddrs()
	result.Addrs = make([]ma.Multiaddr, 0, len(addrs))
	for _, ab := range addrs {
		a, err := ma.NewMultiaddrBytes(ab)
		if err != nil {
			log.Warnf("ignoring unparsable relay address: %s", err)
			continue
		}
		result.Addrs = append(result.Addrs, a)
	}

	voucherBytes := rsvp.GetVoucher()
	if voucherBytes != nil {
		_, rec, err := record.ConsumeEnvelope(voucherBytes, proto.RecordDomain)
		if err != nil {
			return nil, fmt.Errorf("error consuming voucher envelope: %w", err)
		}

		voucher, ok := rec.(*proto.ReservationVoucher)
		if !ok {
			return nil, fmt.Errorf("unexpected voucher record type: %+T", rec)
		}
		result.Voucher = voucher
	}

	limit := msg.GetLimit()
	if limit != nil {
		result.LimitDuration = time.Duration(limit.GetDuration()) * time.Second
		result.LimitData = limit.GetData()
	}

	return result, nil
}
