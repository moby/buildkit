package event

import "github.com/libp2p/go-libp2p/core/network"

// EvtNATDeviceTypeChanged is an event struct to be emitted when the type of the NAT device changes for a Transport Protocol.
//
// Note: This event is meaningful ONLY if the AutoNAT Reachability is Private.
// Consumers of this event should ALSO consume the `EvtLocalReachabilityChanged` event and interpret
// this event ONLY if the Reachability on the `EvtLocalReachabilityChanged` is Private.
type EvtNATDeviceTypeChanged struct {
	// TransportProtocol is the Transport Protocol for which the NAT Device Type has been determined.
	TransportProtocol network.NATTransportProtocol
	// NatDeviceType indicates the type of the NAT Device for the Transport Protocol.
	// Currently, it can be either a `Cone NAT` or a `Symmetric NAT`. Please see the detailed documentation
	// on `network.NATDeviceType` enumerations for a better understanding of what these types mean and
	// how they impact Connectivity and Hole Punching.
	NatDeviceType network.NATDeviceType
}
