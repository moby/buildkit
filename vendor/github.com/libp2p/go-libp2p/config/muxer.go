package config

import (
	"fmt"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	msmux "github.com/libp2p/go-libp2p/p2p/muxer/muxer-multistream"
)

// MuxC is a stream multiplex transport constructor.
type MuxC func(h host.Host) (network.Multiplexer, error)

// MsMuxC is a tuple containing a multiplex transport constructor and a protocol
// ID.
type MsMuxC struct {
	MuxC
	ID string
}

var muxArgTypes = newArgTypeSet(hostType, networkType, peerIDType, pstoreType)

// MuxerConstructor creates a multiplex constructor from the passed parameter
// using reflection.
func MuxerConstructor(m interface{}) (MuxC, error) {
	// Already constructed?
	if t, ok := m.(network.Multiplexer); ok {
		return func(_ host.Host) (network.Multiplexer, error) {
			return t, nil
		}, nil
	}

	ctor, err := makeConstructor(m, muxType, muxArgTypes)
	if err != nil {
		return nil, err
	}
	return func(h host.Host) (network.Multiplexer, error) {
		t, err := ctor(h, nil, nil, nil, nil)
		if err != nil {
			return nil, err
		}
		return t.(network.Multiplexer), nil
	}, nil
}

func makeMuxer(h host.Host, tpts []MsMuxC) (network.Multiplexer, error) {
	muxMuxer := msmux.NewBlankTransport()
	transportSet := make(map[string]struct{}, len(tpts))
	for _, tptC := range tpts {
		if _, ok := transportSet[tptC.ID]; ok {
			return nil, fmt.Errorf("duplicate muxer transport: %s", tptC.ID)
		}
		transportSet[tptC.ID] = struct{}{}
	}
	for _, tptC := range tpts {
		tpt, err := tptC.MuxC(h)
		if err != nil {
			return nil, err
		}
		muxMuxer.AddTransport(tptC.ID, tpt)
	}
	return muxMuxer, nil
}
