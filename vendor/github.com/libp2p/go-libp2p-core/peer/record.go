package peer

import (
	"github.com/libp2p/go-libp2p/core/peer"

	pb "github.com/libp2p/go-libp2p/core/peer/pb"
)

// PeerRecordEnvelopeDomain is the domain string used for peer records contained in a Envelope.
// Deprecated: use github.com/libp2p/go-libp2p/core/peer.PeerRecordEnvelopeDomain instead
const PeerRecordEnvelopeDomain = peer.PeerRecordEnvelopeDomain

// PeerRecordEnvelopePayloadType is the type hint used to identify peer records in a Envelope.
// Defined in https://github.com/multiformats/multicodec/blob/master/table.csv
// with name "libp2p-peer-record".
// Deprecated: use github.com/libp2p/go-libp2p/core/peer.PeerRecordEnvelopePayloadType instead
var PeerRecordEnvelopePayloadType = peer.PeerRecordEnvelopePayloadType

// PeerRecord contains information that is broadly useful to share with other peers,
// either through a direct exchange (as in the libp2p identify protocol), or through
// a Peer Routing provider, such as a DHT.
//
// Currently, a PeerRecord contains the public listen addresses for a peer, but this
// is expected to expand to include other information in the future.
//
// PeerRecords are ordered in time by their Seq field. Newer PeerRecords must have
// greater Seq values than older records. The NewPeerRecord function will create
// a PeerRecord with a timestamp-based Seq value. The other PeerRecord fields should
// be set by the caller:
//
//	rec := peer.NewPeerRecord()
//	rec.PeerID = aPeerID
//	rec.Addrs = someAddrs
//
// Alternatively, you can construct a PeerRecord struct directly and use the TimestampSeq
// helper to set the Seq field:
//
//	rec := peer.PeerRecord{PeerID: aPeerID, Addrs: someAddrs, Seq: peer.TimestampSeq()}
//
// Failing to set the Seq field will not result in an error, however, a PeerRecord with a
// Seq value of zero may be ignored or rejected by other peers.
//
// PeerRecords are intended to be shared with other peers inside a signed
// routing.Envelope, and PeerRecord implements the routing.Record interface
// to facilitate this.
//
// To share a PeerRecord, first call Sign to wrap the record in a Envelope
// and sign it with the local peer's private key:
//
//	rec := &PeerRecord{PeerID: myPeerId, Addrs: myAddrs}
//	envelope, err := rec.Sign(myPrivateKey)
//
// The resulting record.Envelope can be marshalled to a []byte and shared
// publicly. As a convenience, the MarshalSigned method will produce the
// Envelope and marshal it to a []byte in one go:
//
//	rec := &PeerRecord{PeerID: myPeerId, Addrs: myAddrs}
//	recordBytes, err := rec.MarshalSigned(myPrivateKey)
//
// To validate and unmarshal a signed PeerRecord from a remote peer,
// "consume" the containing envelope, which will return both the
// routing.Envelope and the inner Record. The Record must be cast to
// a PeerRecord pointer before use:
//
//	envelope, untypedRecord, err := ConsumeEnvelope(envelopeBytes, PeerRecordEnvelopeDomain)
//	if err != nil {
//	  handleError(err)
//	  return
//	}
//	peerRec := untypedRecord.(*PeerRecord)
//
// Deprecated: use github.com/libp2p/go-libp2p/core/peer.PeerRecord instead
type PeerRecord = peer.PeerRecord

// NewPeerRecord returns a PeerRecord with a timestamp-based sequence number.
// The returned record is otherwise empty and should be populated by the caller.
// Deprecated: use github.com/libp2p/go-libp2p/core/peer.NewPeerRecord instead
func NewPeerRecord() *PeerRecord {
	return peer.NewPeerRecord()
}

// PeerRecordFromAddrInfo creates a PeerRecord from an AddrInfo struct.
// The returned record will have a timestamp-based sequence number.
// Deprecated: use github.com/libp2p/go-libp2p/core/peer.PeerRecordFromAddrInfo instead
func PeerRecordFromAddrInfo(info AddrInfo) *PeerRecord {
	return peer.PeerRecordFromAddrInfo(info)
}

// PeerRecordFromProtobuf creates a PeerRecord from a protobuf PeerRecord
// struct.
// Deprecated: use github.com/libp2p/go-libp2p/core/peer.PeerRecordFromProtobuf instead
func PeerRecordFromProtobuf(msg *pb.PeerRecord) (*PeerRecord, error) {
	return peer.PeerRecordFromProtobuf(msg)
}

// TimestampSeq is a helper to generate a timestamp-based sequence number for a PeerRecord.
// Deprecated: use github.com/libp2p/go-libp2p/core/peer.TimestampSeq instead
func TimestampSeq() uint64 {
	return peer.TimestampSeq()
}
