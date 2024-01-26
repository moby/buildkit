package types

//go:generate protoc --go_out=paths=source_relative:. stat.proto wire.proto

const (
	PACKET_STAT = Packet_PACKET_STAT
	PACKET_REQ  = Packet_PACKET_REQ
	PACKET_DATA = Packet_PACKET_DATA
	PACKET_FIN  = Packet_PACKET_FIN
	PACKET_ERR  = Packet_PACKET_ERR
)
