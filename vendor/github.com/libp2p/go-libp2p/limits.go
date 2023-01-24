package libp2p

import (
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/p2p/host/autonat"
	rcmgr "github.com/libp2p/go-libp2p/p2p/host/resource-manager"
	relayv1 "github.com/libp2p/go-libp2p/p2p/protocol/circuitv1/relay"
	circuit "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/proto"
	relayv2 "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"
	"github.com/libp2p/go-libp2p/p2p/protocol/holepunch"
	"github.com/libp2p/go-libp2p/p2p/protocol/identify"
	"github.com/libp2p/go-libp2p/p2p/protocol/ping"
)

// SetDefaultServiceLimits sets the default limits for bundled libp2p services
func SetDefaultServiceLimits(config *rcmgr.ScalingLimitConfig) {
	// identify
	config.AddServiceLimit(
		identify.ServiceName,
		rcmgr.BaseLimit{StreamsInbound: 64, StreamsOutbound: 64, Streams: 128, Memory: 4 << 20},
		rcmgr.BaseLimitIncrease{StreamsInbound: 64, StreamsOutbound: 64, Streams: 128, Memory: 4 << 20},
	)
	config.AddServicePeerLimit(
		identify.ServiceName,
		rcmgr.BaseLimit{StreamsInbound: 16, StreamsOutbound: 16, Streams: 32, Memory: 1 << 20},
		rcmgr.BaseLimitIncrease{},
	)
	for _, id := range [...]protocol.ID{identify.ID, identify.IDDelta, identify.IDPush} {
		config.AddProtocolLimit(
			id,
			rcmgr.BaseLimit{StreamsInbound: 64, StreamsOutbound: 64, Streams: 128, Memory: 4 << 20},
			rcmgr.BaseLimitIncrease{StreamsInbound: 64, StreamsOutbound: 64, Streams: 128, Memory: 4 << 20},
		)
		config.AddProtocolPeerLimit(
			id,
			rcmgr.BaseLimit{StreamsInbound: 16, StreamsOutbound: 16, Streams: 32, Memory: 32 * (256<<20 + 16<<10)},
			rcmgr.BaseLimitIncrease{},
		)
	}

	//  ping
	addServiceAndProtocolLimit(config,
		ping.ServiceName, ping.ID,
		rcmgr.BaseLimit{StreamsInbound: 64, StreamsOutbound: 64, Streams: 64, Memory: 4 << 20},
		rcmgr.BaseLimitIncrease{StreamsInbound: 64, StreamsOutbound: 64, Streams: 64, Memory: 4 << 20},
	)
	addServicePeerAndProtocolPeerLimit(
		config,
		ping.ServiceName, ping.ID,
		rcmgr.BaseLimit{StreamsInbound: 2, StreamsOutbound: 3, Streams: 4, Memory: 32 * (256<<20 + 16<<10)},
		rcmgr.BaseLimitIncrease{},
	)

	// autonat
	addServiceAndProtocolLimit(config,
		autonat.ServiceName, autonat.AutoNATProto,
		rcmgr.BaseLimit{StreamsInbound: 64, StreamsOutbound: 64, Streams: 64, Memory: 4 << 20},
		rcmgr.BaseLimitIncrease{StreamsInbound: 4, StreamsOutbound: 4, Streams: 4, Memory: 2 << 20},
	)
	addServicePeerAndProtocolPeerLimit(
		config,
		autonat.ServiceName, autonat.AutoNATProto,
		rcmgr.BaseLimit{StreamsInbound: 2, StreamsOutbound: 2, Streams: 2, Memory: 1 << 20},
		rcmgr.BaseLimitIncrease{},
	)

	// holepunch
	addServiceAndProtocolLimit(config,
		holepunch.ServiceName, holepunch.Protocol,
		rcmgr.BaseLimit{StreamsInbound: 32, StreamsOutbound: 32, Streams: 64, Memory: 4 << 20},
		rcmgr.BaseLimitIncrease{StreamsInbound: 8, StreamsOutbound: 8, Streams: 16, Memory: 4 << 20},
	)
	addServicePeerAndProtocolPeerLimit(config,
		holepunch.ServiceName, holepunch.Protocol,
		rcmgr.BaseLimit{StreamsInbound: 2, StreamsOutbound: 2, Streams: 2, Memory: 1 << 20},
		rcmgr.BaseLimitIncrease{},
	)

	// relay/v1
	config.AddServiceLimit(
		relayv1.ServiceName,
		rcmgr.BaseLimit{StreamsInbound: 256, StreamsOutbound: 256, Streams: 256, Memory: 16 << 20},
		rcmgr.BaseLimitIncrease{StreamsInbound: 256, StreamsOutbound: 256, Streams: 256, Memory: 16 << 20},
	)
	config.AddServicePeerLimit(
		relayv1.ServiceName,
		rcmgr.BaseLimit{StreamsInbound: 64, StreamsOutbound: 64, Streams: 64, Memory: 1 << 20},
		rcmgr.BaseLimitIncrease{},
	)

	// relay/v2
	config.AddServiceLimit(
		relayv2.ServiceName,
		rcmgr.BaseLimit{StreamsInbound: 256, StreamsOutbound: 256, Streams: 256, Memory: 16 << 20},
		rcmgr.BaseLimitIncrease{StreamsInbound: 256, StreamsOutbound: 256, Streams: 256, Memory: 16 << 20},
	)
	config.AddServicePeerLimit(
		relayv2.ServiceName,
		rcmgr.BaseLimit{StreamsInbound: 64, StreamsOutbound: 64, Streams: 64, Memory: 1 << 20},
		rcmgr.BaseLimitIncrease{},
	)

	// circuit protocols, both client and service
	for _, proto := range [...]protocol.ID{circuit.ProtoIDv1, circuit.ProtoIDv2Hop, circuit.ProtoIDv2Stop} {
		config.AddProtocolLimit(
			proto,
			rcmgr.BaseLimit{StreamsInbound: 640, StreamsOutbound: 640, Streams: 640, Memory: 16 << 20},
			rcmgr.BaseLimitIncrease{StreamsInbound: 640, StreamsOutbound: 640, Streams: 640, Memory: 16 << 20},
		)
		config.AddProtocolPeerLimit(
			proto,
			rcmgr.BaseLimit{StreamsInbound: 128, StreamsOutbound: 128, Streams: 128, Memory: 32 << 20},
			rcmgr.BaseLimitIncrease{},
		)
	}
}

func addServiceAndProtocolLimit(config *rcmgr.ScalingLimitConfig, service string, proto protocol.ID, limit rcmgr.BaseLimit, increase rcmgr.BaseLimitIncrease) {
	config.AddServiceLimit(service, limit, increase)
	config.AddProtocolLimit(proto, limit, increase)
}

func addServicePeerAndProtocolPeerLimit(config *rcmgr.ScalingLimitConfig, service string, proto protocol.ID, limit rcmgr.BaseLimit, increase rcmgr.BaseLimitIncrease) {
	config.AddServicePeerLimit(service, limit, increase)
	config.AddProtocolPeerLimit(proto, limit, increase)
}
