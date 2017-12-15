package netlink

import (
	"encoding/binary"
	"errors"
)

var (
	// ErrAttrHeaderTruncated is returned when a netlink attribute's header is
	// truncated.
	ErrAttrHeaderTruncated = errors.New("attribute header truncated")
	// ErrAttrBodyTruncated is returned when a netlink attribute's body is
	// truncated.
	ErrAttrBodyTruncated = errors.New("attribute body truncated")
)

type Fou struct {
	Family    int
	Port      int
	Protocol  int
	EncapType int
}

func deserializeFouMsg(msg []byte) (Fou, error) {
	// we'll skip to byte 4 to first attribute
	msg = msg[3:]
	var shift int
	fou := Fou{}

	for {
		// attribute header is at least 16 bits
		if len(msg) < 4 {
			return fou, ErrAttrHeaderTruncated
		}

		lgt := int(binary.BigEndian.Uint16(msg[0:2]))
		if len(msg) < lgt+4 {
			return fou, ErrAttrBodyTruncated
		}
		attr := binary.BigEndian.Uint16(msg[2:4])

		shift = lgt + 3
		switch attr {
		case FOU_ATTR_AF:
			fou.Family = int(msg[5])
		case FOU_ATTR_PORT:
			fou.Port = int(binary.BigEndian.Uint16(msg[5:7]))
			// port is 2 bytes
			shift = lgt + 2
		case FOU_ATTR_IPPROTO:
			fou.Protocol = int(msg[5])
		case FOU_ATTR_TYPE:
			fou.EncapType = int(msg[5])
		}

		msg = msg[shift:]

		if len(msg) < 4 {
			break
		}
	}

	return fou, nil
}
