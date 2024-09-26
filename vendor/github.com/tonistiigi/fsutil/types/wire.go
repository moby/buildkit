package types

import "google.golang.org/protobuf/proto"

const (
	PACKET_STAT = Packet_PACKET_STAT
	PACKET_REQ  = Packet_PACKET_REQ
	PACKET_DATA = Packet_PACKET_DATA
	PACKET_FIN  = Packet_PACKET_FIN
	PACKET_ERR  = Packet_PACKET_ERR
)

func (p *Packet) Marshal() ([]byte, error) {
	return proto.MarshalOptions{Deterministic: true}.Marshal(p)
}

func (p *Packet) Unmarshal(dAtA []byte) error {
	return proto.UnmarshalOptions{Merge: true}.Unmarshal(dAtA, p)
}

func (p *Packet) Size() int {
	return proto.Size(p)
}
