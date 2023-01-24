package client

import (
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	pbv1 "github.com/libp2p/go-libp2p/p2p/protocol/circuitv1/pb"
	pbv2 "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/pb"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/util"
)

var (
	StreamTimeout = 1 * time.Minute
	AcceptTimeout = 10 * time.Second
)

func (c *Client) handleStreamV2(s network.Stream) {
	log.Debugf("new relay/v2 stream from: %s", s.Conn().RemotePeer())

	s.SetReadDeadline(time.Now().Add(StreamTimeout))

	rd := util.NewDelimitedReader(s, maxMessageSize)
	defer rd.Close()

	writeResponse := func(status pbv2.Status) error {
		wr := util.NewDelimitedWriter(s)

		var msg pbv2.StopMessage
		msg.Type = pbv2.StopMessage_STATUS.Enum()
		msg.Status = status.Enum()

		return wr.WriteMsg(&msg)
	}

	handleError := func(status pbv2.Status) {
		log.Debugf("protocol error: %s (%d)", pbv2.Status_name[int32(status)], status)
		err := writeResponse(status)
		if err != nil {
			s.Reset()
			log.Debugf("error writing circuit response: %s", err.Error())
		} else {
			s.Close()
		}
	}

	var msg pbv2.StopMessage

	err := rd.ReadMsg(&msg)
	if err != nil {
		handleError(pbv2.Status_MALFORMED_MESSAGE)
		return
	}
	// reset stream deadline as message has been read
	s.SetReadDeadline(time.Time{})

	if msg.GetType() != pbv2.StopMessage_CONNECT {
		handleError(pbv2.Status_UNEXPECTED_MESSAGE)
		return
	}

	src, err := util.PeerToPeerInfoV2(msg.GetPeer())
	if err != nil {
		handleError(pbv2.Status_MALFORMED_MESSAGE)
		return
	}

	// check for a limit provided by the relay; if the limit is not nil, then this is a limited
	// relay connection and we mark the connection as transient.
	var stat network.ConnStats
	if limit := msg.GetLimit(); limit != nil {
		stat.Transient = true
		stat.Extra = make(map[interface{}]interface{})
		stat.Extra[StatLimitDuration] = time.Duration(limit.GetDuration()) * time.Second
		stat.Extra[StatLimitData] = limit.GetData()
	}

	log.Debugf("incoming relay connection from: %s", src.ID)

	select {
	case c.incoming <- accept{
		conn: &Conn{stream: s, remote: src, stat: stat, client: c},
		writeResponse: func() error {
			return writeResponse(pbv2.Status_OK)
		},
	}:
	case <-time.After(AcceptTimeout):
		handleError(pbv2.Status_CONNECTION_FAILED)
	}
}

func (c *Client) handleStreamV1(s network.Stream) {
	log.Debugf("new relay/v1 stream from: %s", s.Conn().RemotePeer())

	s.SetReadDeadline(time.Now().Add(StreamTimeout))

	rd := util.NewDelimitedReader(s, maxMessageSize)
	defer rd.Close()

	writeResponse := func(status pbv1.CircuitRelay_Status) error {
		wr := util.NewDelimitedWriter(s)

		var msg pbv1.CircuitRelay
		msg.Type = pbv1.CircuitRelay_STATUS.Enum()
		msg.Code = status.Enum()

		return wr.WriteMsg(&msg)
	}

	handleError := func(status pbv1.CircuitRelay_Status) {
		log.Debugf("protocol error: %s (%d)", pbv1.CircuitRelay_Status_name[int32(status)], status)
		err := writeResponse(status)
		if err != nil {
			s.Reset()
			log.Debugf("error writing circuit response: %s", err.Error())
		} else {
			s.Close()
		}
	}

	var msg pbv1.CircuitRelay

	err := rd.ReadMsg(&msg)
	if err != nil {
		handleError(pbv1.CircuitRelay_MALFORMED_MESSAGE)
		return
	}
	// reset stream deadline as message has been read
	s.SetReadDeadline(time.Time{})

	switch msg.GetType() {
	case pbv1.CircuitRelay_STOP:

	case pbv1.CircuitRelay_HOP:
		handleError(pbv1.CircuitRelay_HOP_CANT_SPEAK_RELAY)
		return

	case pbv1.CircuitRelay_CAN_HOP:
		handleError(pbv1.CircuitRelay_HOP_CANT_SPEAK_RELAY)
		return

	default:
		log.Debugf("unexpected relay handshake: %d", msg.GetType())
		handleError(pbv1.CircuitRelay_MALFORMED_MESSAGE)
		return
	}

	src, err := util.PeerToPeerInfoV1(msg.GetSrcPeer())
	if err != nil {
		handleError(pbv1.CircuitRelay_STOP_SRC_MULTIADDR_INVALID)
		return
	}

	dst, err := util.PeerToPeerInfoV1(msg.GetDstPeer())
	if err != nil || dst.ID != c.host.ID() {
		handleError(pbv1.CircuitRelay_STOP_DST_MULTIADDR_INVALID)
		return
	}

	log.Debugf("incoming relay connection from: %s", src.ID)

	select {
	case c.incoming <- accept{
		conn: &Conn{stream: s, remote: src, client: c},
		writeResponse: func() error {
			return writeResponse(pbv1.CircuitRelay_SUCCESS)
		},
	}:
	case <-time.After(AcceptTimeout):
		handleError(pbv1.CircuitRelay_STOP_RELAY_REFUSED)
	}
}
