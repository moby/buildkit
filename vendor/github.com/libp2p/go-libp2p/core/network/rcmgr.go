package network

import (
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/multiformats/go-multiaddr"
)

// ResourceManager is the interface to the network resource management subsystem.
// The ResourceManager tracks and accounts for resource usage in the stack, from the internals
// to the application, and provides a mechanism to limit resource usage according to a user
// configurable policy.
//
// Resource Management through the ResourceManager is based on the concept of Resource
// Management Scopes, whereby resource usage is constrained by a DAG of scopes,
// The following diagram illustrates the structure of the resource constraint DAG:
// System
//
//	+------------> Transient.............+................+
//	|                                    .                .
//	+------------>  Service------------- . ----------+    .
//	|                                    .           |    .
//	+------------->  Protocol----------- . ----------+    .
//	|                                    .           |    .
//	+-------------->  Peer               \           |    .
//	                   +------------> Connection     |    .
//	                   |                             \    \
//	                   +--------------------------->  Stream
//
// The basic resources accounted by the ResourceManager include memory, streams, connections,
// and file  descriptors. These account for both space and time used by
// the stack, as each resource has a direct effect on the system
// availability and performance.
//
// The modus operandi of the resource manager is to restrict resource usage at the time of
// reservation. When a component of the stack needs to use a resource, it reserves it in the
// appropriate scope. The resource manager gates the reservation against the scope applicable
// limits; if the limit is exceeded, then an error (wrapping ErrResourceLimitExceeded) and it
// is up the component to act accordingly. At the lower levels of the stack, this will normally
// signal a failure of some sorts, like failing to opening a stream or a connection, which will
// propagate to the programmer. Some components may be able to handle resource reservation failure
// more gracefully; for instance a muxer trying to grow a buffer for a window change, will simply
// retain the existing window size and continue to operate normally albeit with some degraded
// throughput.
// All resources reserved in some scope are released when the scope is closed. For low level
// scopes, mainly Connection and Stream scopes, this happens when the connection or stream is
// closed.
//
// Service programmers will typically use the resource manager to reserve memory
// for their subsystem.
// This happens with two avenues: the programmer can attach a stream to a service, whereby
// resources reserved by the stream are automatically accounted in the service budget; or the
// programmer may directly interact with the service scope, by using ViewService through the
// resource manager interface.
//
// Application programmers can also directly reserve memory in some applicable scope. In order
// to facilitate control flow delimited resource accounting, all scopes defined in the system
// allow for the user to create spans. Spans are temporary scopes rooted at some
// other scope and release their resources when the programmer is done with them. Span
// scopes can form trees, with nested spans.
//
// Typical Usage:
//   - Low level components of the system (transports, muxers) all have access to the resource
//     manager and create connection and stream scopes through it. These scopes are accessible
//     to the user, albeit with a narrower interface, through Conn and Stream objects who have
//     a Scope method.
//   - Services typically center around streams, where the programmer can attach streams to a
//     particular service. They can also directly reserve memory for a service by accessing the
//     service scope using the ResourceManager interface.
//   - Applications that want to account for their network resource usage can reserve memory,
//     typically using a span, directly in the System or a Service scope; they can also
//     opt to use appropriate steam scopes for streams that they create or own.
//
// User Serviceable Parts: the user has the option to specify their own implementation of the
// interface. We provide a canonical implementation in the go-libp2p-resource-manager package.
// The user of that package can specify limits for the various scopes, which can be static
// or dynamic.
//
// WARNING The ResourceManager interface is considered experimental and subject to change
//
//	in subsequent releases.
type ResourceManager interface {
	ResourceScopeViewer

	// OpenConnection creates a new connection scope not yet associated with any peer; the connection
	// is scoped at the transient scope.
	// The caller owns the returned scope and is responsible for calling Done in order to signify
	// the end of the scope's span.
	OpenConnection(dir Direction, usefd bool, endpoint multiaddr.Multiaddr) (ConnManagementScope, error)

	// OpenStream creates a new stream scope, initially unnegotiated.
	// An unnegotiated stream will be initially unattached to any protocol scope
	// and constrained by the transient scope.
	// The caller owns the returned scope and is responsible for calling Done in order to signify
	// the end of th scope's span.
	OpenStream(p peer.ID, dir Direction) (StreamManagementScope, error)

	// Close closes the resource manager
	Close() error
}

// ResourceScopeViewer is a mixin interface providing view methods for accessing top level
// scopes.
type ResourceScopeViewer interface {
	// ViewSystem views the system wide resource scope.
	// The system scope is the top level scope that accounts for global
	// resource usage at all levels of the system. This scope constrains all
	// other scopes and institutes global hard limits.
	ViewSystem(func(ResourceScope) error) error

	// ViewTransient views the transient (DMZ) resource scope.
	// The transient scope accounts for resources that are in the process of
	// full establishment.  For instance, a new connection prior to the
	// handshake does not belong to any peer, but it still needs to be
	// constrained as this opens an avenue for attacks in transient resource
	// usage. Similarly, a stream that has not negotiated a protocol yet is
	// constrained by the transient scope.
	ViewTransient(func(ResourceScope) error) error

	// ViewService retrieves a service-specific scope.
	ViewService(string, func(ServiceScope) error) error

	// ViewProtocol views the resource management scope for a specific protocol.
	ViewProtocol(protocol.ID, func(ProtocolScope) error) error

	// ViewPeer views the resource management scope for a specific peer.
	ViewPeer(peer.ID, func(PeerScope) error) error
}

const (
	// ReservationPriorityLow is a reservation priority that indicates a reservation if the scope
	// memory utilization is at 40% or less.
	ReservationPriorityLow uint8 = 101
	// Reservation PriorityMedium is a reservation priority that indicates a reservation if the scope
	// memory utilization is at 60% or less.
	ReservationPriorityMedium uint8 = 152
	// ReservationPriorityHigh is a reservation prioirity that indicates a reservation if the scope
	// memory utilization is at 80% or less.
	ReservationPriorityHigh uint8 = 203
	// ReservationPriorityAlways is a reservation priority that indicates a reservation if there is
	// enough memory, regardless of scope utilization.
	ReservationPriorityAlways uint8 = 255
)

// ResourceScope is the interface for all scopes.
type ResourceScope interface {
	// ReserveMemory reserves memory/buffer space in the scope; the unit is bytes.
	//
	// If ReserveMemory returns an error, then no memory was reserved and the caller should handle
	// the failure condition.
	//
	// The priority argument indicates the priority of the memory reservation. A reservation
	// will fail if the available memory is less than (1+prio)/256 of the scope limit, providing
	// a mechanism to gracefully handle optional reservations that might overload the system.
	// For instance, a muxer growing a window buffer will use a low priority and only grow the buffer
	// if there is no memory pressure in the system.
	//
	// The are 4 predefined priority levels, Low, Medium, High and Always,
	// capturing common patterns, but the user is free to use any granularity applicable to his case.
	ReserveMemory(size int, prio uint8) error

	// ReleaseMemory explicitly releases memory previously reserved with ReserveMemory
	ReleaseMemory(size int)

	// Stat retrieves current resource usage for the scope.
	Stat() ScopeStat

	// BeginSpan creates a new span scope rooted at this scope
	BeginSpan() (ResourceScopeSpan, error)
}

// ResourceScopeSpan is a ResourceScope with a delimited span.
// Span scopes are control flow delimited and release all their associated resources
// when the programmer calls Done.
//
// Example:
//
//	s, err := someScope.BeginSpan()
//	if err != nil { ... }
//	defer s.Done()
//
//	if err := s.ReserveMemory(...); err != nil { ... }
//	// ... use memory
type ResourceScopeSpan interface {
	ResourceScope
	// Done ends the span and releases associated resources.
	Done()
}

// ServiceScope is the interface for service resource scopes
type ServiceScope interface {
	ResourceScope

	// Name returns the name of this service
	Name() string
}

// ProtocolScope is the interface for protocol resource scopes.
type ProtocolScope interface {
	ResourceScope

	// Protocol returns the protocol for this scope
	Protocol() protocol.ID
}

// PeerScope is the interface for peer resource scopes.
type PeerScope interface {
	ResourceScope

	// Peer returns the peer ID for this scope
	Peer() peer.ID
}

// ConnManagementScope is the low level interface for connection resource scopes.
// This interface is used by the low level components of the system who create and own
// the span of a connection scope.
type ConnManagementScope interface {
	ResourceScopeSpan

	// PeerScope returns the peer scope associated with this connection.
	// It returns nil if the connection is not yet asociated with any peer.
	PeerScope() PeerScope

	// SetPeer sets the peer for a previously unassociated connection
	SetPeer(peer.ID) error
}

// ConnScope is the user view of a connection scope
type ConnScope interface {
	ResourceScope
}

// StreamManagementScope is the interface for stream resource scopes.
// This interface is used by the low level components of the system who create and own
// the span of a stream scope.
type StreamManagementScope interface {
	ResourceScopeSpan

	// ProtocolScope returns the protocol resource scope associated with this stream.
	// It returns nil if the stream is not associated with any protocol scope.
	ProtocolScope() ProtocolScope
	// SetProtocol sets the protocol for a previously unnegotiated stream
	SetProtocol(proto protocol.ID) error

	// ServiceScope returns the service owning the stream, if any.
	ServiceScope() ServiceScope
	// SetService sets the service owning this stream.
	SetService(srv string) error

	// PeerScope returns the peer resource scope associated with this stream.
	PeerScope() PeerScope
}

// StreamScope is the user view of a StreamScope.
type StreamScope interface {
	ResourceScope

	// SetService sets the service owning this stream.
	SetService(srv string) error
}

// ScopeStat is a struct containing resource accounting information.
type ScopeStat struct {
	NumStreamsInbound  int
	NumStreamsOutbound int
	NumConnsInbound    int
	NumConnsOutbound   int
	NumFD              int

	Memory int64
}

// NullResourceManager is a stub for tests and initialization of default values
var NullResourceManager ResourceManager = &nullResourceManager{}

type nullResourceManager struct{}
type nullScope struct{}

var _ ResourceScope = (*nullScope)(nil)
var _ ResourceScopeSpan = (*nullScope)(nil)
var _ ServiceScope = (*nullScope)(nil)
var _ ProtocolScope = (*nullScope)(nil)
var _ PeerScope = (*nullScope)(nil)
var _ ConnManagementScope = (*nullScope)(nil)
var _ ConnScope = (*nullScope)(nil)
var _ StreamManagementScope = (*nullScope)(nil)
var _ StreamScope = (*nullScope)(nil)

// NullScope is a stub for tests and initialization of default values
var NullScope = &nullScope{}

func (n *nullResourceManager) ViewSystem(f func(ResourceScope) error) error {
	return f(NullScope)
}
func (n *nullResourceManager) ViewTransient(f func(ResourceScope) error) error {
	return f(NullScope)
}
func (n *nullResourceManager) ViewService(svc string, f func(ServiceScope) error) error {
	return f(NullScope)
}
func (n *nullResourceManager) ViewProtocol(p protocol.ID, f func(ProtocolScope) error) error {
	return f(NullScope)
}
func (n *nullResourceManager) ViewPeer(p peer.ID, f func(PeerScope) error) error {
	return f(NullScope)
}
func (n *nullResourceManager) OpenConnection(dir Direction, usefd bool, endpoint multiaddr.Multiaddr) (ConnManagementScope, error) {
	return NullScope, nil
}
func (n *nullResourceManager) OpenStream(p peer.ID, dir Direction) (StreamManagementScope, error) {
	return NullScope, nil
}
func (n *nullResourceManager) Close() error {
	return nil
}

func (n *nullScope) ReserveMemory(size int, prio uint8) error { return nil }
func (n *nullScope) ReleaseMemory(size int)                   {}
func (n *nullScope) Stat() ScopeStat                          { return ScopeStat{} }
func (n *nullScope) BeginSpan() (ResourceScopeSpan, error)    { return NullScope, nil }
func (n *nullScope) Done()                                    {}
func (n *nullScope) Name() string                             { return "" }
func (n *nullScope) Protocol() protocol.ID                    { return "" }
func (n *nullScope) Peer() peer.ID                            { return "" }
func (n *nullScope) PeerScope() PeerScope                     { return NullScope }
func (n *nullScope) SetPeer(peer.ID) error                    { return nil }
func (n *nullScope) ProtocolScope() ProtocolScope             { return NullScope }
func (n *nullScope) SetProtocol(proto protocol.ID) error      { return nil }
func (n *nullScope) ServiceScope() ServiceScope               { return NullScope }
func (n *nullScope) SetService(srv string) error              { return nil }
