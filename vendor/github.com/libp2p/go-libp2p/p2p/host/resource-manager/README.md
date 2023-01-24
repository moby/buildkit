# The libp2p Network Resource Manager

This package contains the canonical implementation of the libp2p
Network Resource Manager interface.

The implementation is based on the concept of Resource Management
Scopes, whereby resource usage is constrained by a DAG of scopes,
accounting for multiple levels of resource constraints.

The Resource Manager doesn't prioritize resource requests at all, it simply
checks if the resource being requested is currently below the defined limits and
returns an error if the limit is reached. It has no notion of honest vs bad peers.

The Resource Manager does have a special notion of [allowlisted](#allowlisting-multiaddrs-to-mitigate-eclipse-attacks) multiaddrs that
have their own limits if the normal system limits are reached.

## Usage

The Resource Manager is intended to be used with go-libp2p. go-libp2p sets up a
resource manager with the default autoscaled limits if none is provided, but if
you want to configure things or if you want to enable metrics you'll use the
resource manager like so:

```go
// Start with the default scaling limits.
scalingLimits := rcmgr.DefaultLimits

// Add limits around included libp2p protocols
libp2p.SetDefaultServiceLimits(&scalingLimits)

// Turn the scaling limits into a static set of limits using `.AutoScale`. This
// scales the limits proportional to your system memory.
limits := scalingLimits.AutoScale()

// The resource manager expects a limiter, se we create one from our limits.
limiter := rcmgr.NewFixedLimiter(limits)

// (Optional if you want metrics) Construct the OpenCensus metrics reporter.
str, err := rcmgrObs.NewStatsTraceReporter()
if err != nil {
  panic(err)
}

// Initialize the resource manager
rm, err := rcmgr.NewResourceManager(limiter, rcmgr.WithTraceReporter(str))
if err != nil {
  panic(err)
}

// Create a libp2p host
host, err := libp2p.New(libp2p.ResourceManager(rm))
```

## Basic Resources

### Memory

Perhaps the most fundamental resource is memory, and in particular
buffers used for network operations. The system must provide an
interface for components to reserve memory that accounts for buffers
(and possibly other live objects), which is scoped within the component.
Before a new buffer is allocated, the component should try a memory
reservation, which can fail if the resource limit is exceeded. It is
then up to the component to react to the error condition, depending on
the situation. For example, a muxer failing to grow a buffer in
response to a window change should simply retain the old buffer and
operate at perhaps degraded performance.

### File Descriptors

File descriptors are an important resource that uses memory (and
computational time) at the system level. They are also a scarce
resource, as typically (unless the user explicitly intervenes) they
are constrained by the system. Exhaustion of file descriptors may
render the application incapable of operating (e.g., because it is
unable to open a file).  This is important for libp2p because most
operating systems represent sockets as file descriptors.

### Connections

Connections are a higher-level concept endemic to libp2p; in order to
communicate with another peer, a connection must first be
established. Connections are an important resource in libp2p, as they
consume memory, goroutines, and possibly file descriptors.

We distinguish between inbound and outbound connections, as the former
are initiated by remote peers and consume resources in response to
network events and thus need to be tightly controlled in order to
protect the application from overload or attack.  Outbound
connections are typically initiated by the application's volition and
don't need to be controlled as tightly. However, outbound connections
still consume resources and may be initiated in response to network
events because of (potentially faulty) application logic, so they
still need to be constrained.

### Streams

Streams are the fundamental object of interaction in libp2p; all
protocol interactions happen through a stream that goes over some
connection. Streams are a fundamental resource in libp2p, as they
consume memory and goroutines at all levels of the stack.

Streams always belong to a peer, specify a protocol and they may
belong to some service in the system. Hence, this suggests that apart
from global limits, we can constrain stream usage at finer
granularity, at the protocol and service level.

Once again, we disinguish between inbound and outbound streams.
Inbound streams are initiated by remote peers and consume resources in
response to network events; controlling inbound stream usage is again
paramount for protecting the system from overload or attack.
Outbound streams are normally initiated by the application or some
service in the system in order to effect some protocol
interaction. However, they can also be initiated in response to
network events because of application or service logic, so we still
need to constrain them.


## Resource Scopes

The Resource Manager is based on the concept of resource
scopes. Resource Scopes account for resource usage that is temporally
delimited for the span of the scope. Resource Scopes conceptually
form a DAG, providing us with a mechanism to enforce multiresolution
resource accounting. Downstream resource usage is aggregated at scopes
higher up the graph.

The following diagram depicts the canonical scope graph:
```
System
  +------------> Transient.............+................+
  |                                    .                .
  +------------>  Service------------- . ----------+    .
  |                                    .           |    .
  +------------->  Protocol----------- . ----------+    .
  |                                    .           |    .
  +-------------->* Peer               \/          |    .
                     +------------> Connection     |    .
                     |                             \/   \/
                     +--------------------------->  Stream
```

### The System Scope

The system scope is the top level scope that accounts for global
resource usage at all levels of the system. This scope nests and
constrains all other scopes and institutes global hard limits.

### The Transient Scope

The transient scope accounts for resources that are in the process of
full establishment.  For instance, a new connection prior to the
handshake does not belong to any peer, but it still needs to be
constrained as this opens an avenue for attacks in transient resource
usage. Similarly, a stream that has not negotiated a protocol yet is
constrained by the transient scope.

The transient scope effectively represents a DMZ (DeMilitarized Zone),
where resource usage can be accounted for connections and streams that
are not fully established.

### The Allowlist System Scope

Same as the normal system scope above, but is used if the normal system scope is
already at its limits and the resource is from an allowlisted peer. See
[Allowlisting multiaddrs to mitigate eclipse
attacks](#allowlisting-multiaddrs-to-mitigate-eclipse-attacks) see for more
information.

### The Allowlist Transient Scope

Same as the normal transient scope above, but is used if the normal transient
scope is already at its limits and the resource is from an allowlisted peer. See
[Allowlisting multiaddrs to mitigate eclipse
attacks](#allowlisting-multiaddrs-to-mitigate-eclipse-attacks) see for more
information.

### Service Scopes

The system is typically organized across services, which may be
ambient and provide basic functionality to the system (e.g. identify,
autonat, relay, etc). Alternatively, services may be explicitly
instantiated by the application, and provide core components of its
functionality (e.g. pubsub, the DHT, etc).

Services are logical groupings of streams that implement protocol flow
and may additionally consume resources such as memory. Services
typically have at least one stream handler, so they are subject to
inbound stream creation and resource usage in response to network
events. As such, the system explicitly models them allowing for
isolated resource usage that can be tuned by the user.

### Protocol Scopes

Protocol Scopes account for resources at the protocol level. They are
an intermediate resource scope which can constrain streams which may
not have a service associated or for resource control within a
service. It also provides an opportunity for system operators to
explicitly restrict specific protocols.

For instance, a service that is not aware of the resource manager and
has not been ported to mark its streams, may still gain limits
transparently without any programmer intervention.  Furthermore, the
protocol scope can constrain resource usage for services that
implement multiple protocols for the sake of backwards
compatibility. A tighter limit in some older protocol can protect the
application from resource consumption caused by legacy clients or
potential attacks.

For a concrete example, consider pubsub with the gossipsub router: the
service also understands the floodsub protocol for backwards
compatibility and support for unsophisticated clients that are lagging
in the implementation effort. By specifying a lower limit for the
floodsub protocol, we can can constrain the service level for legacy
clients using an inefficient protocol.

### Peer Scopes

The peer scope accounts for resource usage by an individual peer. This
constrains connections and streams and limits the blast radius of
resource consumption by a single remote peer.

This ensures that no single peer can use more resources than allowed
by the peer limits. Every peer has a default limit, but the programmer
may raise (or lower) limits for specific peers.


### Connection Scopes

The connection scope is delimited to the duration of a connection and
constrains resource usage by a single connection. The scope is a leaf
in the DAG, with a span that begins when a connection is established
and ends when the connection is closed. Its resources are aggregated
to the resource usage of a peer.

### Stream Scopes

The stream scope is delimited to the duration of a stream, and
constrains resource usage by a single stream. This scope is also a
leaf in the DAG, with span that begins when a stream is created and
ends when the stream is closed. Its resources are aggregated to the
resource usage of a peer, and constrained by a service and protocol
scope.

### User Transaction Scopes

User transaction scopes can be created as a child of any extant
resource scope, and provide the programmer with a delimited scope for
easy resource accounting. Transactions may form a tree that is rooted
to some canonical scope in the scope DAG.

For instance, a programmer may create a transaction scope within a
service that accounts for some control flow delimited resource
usage. Similarly, a programmer may create a transaction scope for some
interaction within a stream, e.g. a Request/Response interaction that
uses a buffer.

## Limits

Each resource scope has an associated limit object, which designates
limits for all [basic resources](#basic-resources).  The limit is checked every time some
resource is reserved and provides the system with an opportunity to
constrain resource usage.

There are separate limits for each class of scope, allowing for
multiresolution and aggregate resource accounting.  As such, we have
limits for the system and transient scopes, default and specific
limits for services, protocols, and peers, and limits for connections
and streams.

### Scaling Limits

When building software that is supposed to run on many different kind of machines,
with various memory and CPU configurations, it is desireable to have limits that
scale with the size of the machine.

This is done using the `ScalingLimitConfig`. For every scope, this configuration
struct defines the absolutely bare minimum limits, and an (optional) increase of
these limits, which will be applied on nodes that have sufficient memory.

A `ScalingLimitConfig` can be converted into a `LimitConfig` (which can then be
used to initialize a fixed limiter with `NewFixedLimiter`) by calling the `Scale` method.
The `Scale` method takes two parameters: the amount of memory and the number of file
descriptors that an application is willing to dedicate to libp2p.

These amounts will differ between use cases. A blockchain node running on a dedicated
server might have a lot of memory, and dedicate 1/4 of that memory to libp2p. On the
other end of the spectrum, a desktop companion application running as a background
task on a consumer laptop will probably dedicate significantly less than 1/4 of its system
memory to libp2p.

For convenience, the `ScalingLimitConfig` also provides an `AutoScale` method,
which determines the amount of memory and file descriptors available on the
system, and dedicates up to 1/8 of the memory and 1/2 of the file descriptors to
libp2p.

For example, one might set:
```go
var scalingLimits = ScalingLimitConfig{
  SystemBaseLimit: BaseLimit{
    ConnsInbound:    64,
    ConnsOutbound:   128,
    Conns:           128,
    StreamsInbound:  512,
    StreamsOutbound: 1024,
    Streams:         1024,
    Memory:          128 << 20,
    FD:              256,
  },
  SystemLimitIncrease: BaseLimitIncrease{
    ConnsInbound:    32,
    ConnsOutbound:   64,
    Conns:           64,
    StreamsInbound:  256,
    StreamsOutbound: 512,
    Streams:         512,
    Memory:          256 << 20,
    FDFraction:      1,
  },
}
```

The base limit (`SystemBaseLimit`) here is the minimum configuration that any
node will have, no matter how little memory it possesses. For every GB of memory
passed into the `Scale` method, an increase of (`SystemLimitIncrease`) is added.

For Example, calling `Scale` with 4 GB of memory will result in a limit of 384 for
`Conns` (128 + 4*64).

The `FDFraction` defines how many of the file descriptors are allocated to this
scope. In the example above, when called with a file descriptor value of 1000,
this would result in a limit of 1000 (1000 * 1) file descriptors for the system
scope. See `TestReadmeExample` in `limit_test.go`.

Note that we only showed the configuration for the system scope here, equivalent
configuration options apply to all other scopes as well.

### Default limits

By default the resource manager ships with some reasonable scaling limits and
makes a reasonable guess at how much system memory you want to dedicate to the
go-libp2p process. For the default definitions see [`DefaultLimits` and
`ScalingLimitConfig.AutoScale()`](./limit_defaults.go).

### Tweaking Defaults

If the defaults seem mostly okay, but you want to adjust one facet you can
simply copy the default struct object and update the field you want to change. You can
apply changes to a `BaseLimit`, `BaseLimitIncrease`, and `LimitConfig` with
`.Apply`.

Example
```
// An example on how to tweak the default limits
tweakedDefaults := DefaultLimits
tweakedDefaults.ProtocolBaseLimit.Streams = 1024
tweakedDefaults.ProtocolBaseLimit.StreamsInbound = 512
tweakedDefaults.ProtocolBaseLimit.StreamsOutbound = 512
```

### How to tune your limits

Once you've set your limits and monitoring (see [Monitoring](#monitoring) below)
you can now tune your limits better.  The `rcmgr_blocked_resources` metric will
tell you what was blocked and for what scope. If you see a steady stream of
these blocked requests it means your resource limits are too low for your usage.
If you see a rare sudden spike, this is okay and it means the resource manager
protected you from some anomaly.

### How to disable limits

Sometimes disabling all limits is useful when you want to see how much
resources you use during normal operation. You can then use this information to
define your initial limits. Disable the limits by using `InfiniteLimits`.

### Debug "resource limit exceeded" errors

These errors occur whenever a limit is hit. For example, you'll get this error if
you are at your limit for the number of streams you can have, and you try to
open one more.

Example Log:
```
2022-08-12T15:49:35.459-0700	DEBUG	rcmgr	go-libp2p-resource-manager@v0.5.3/scope.go:541	blocked connection from constraining edge	{"scope": "conn-19667", "edge": "system", "direction": "Inbound", "usefd": false, "current": 100, "attempted": 1, "limit": 100, "stat": {"NumStreamsInbound":28,"NumStreamsOutbound":66,"NumConnsInbound":37,"NumConnsOutbound":63,"NumFD":33,"Memory":8687616}, "error": "system: cannot reserve connection: resource limit exceeded"}
```

The log line above is an example log line that gets emitted if you enable debug
logging in the resource manager. You can do this by setting the environment
variable `GOLOG_LOG_LEVEL="rcmgr=info"`. By default only the error is
returned to the caller, and nothing is logged by the resource manager itself.

The log line message (and returned error) will tell you which resource limit was
hit (connection in the log above) and what blocked it (in this case it was the
system scope that blocked it). The log will also include some more information
about the current usage of the resources. In the example log above, there is a
limit of 100 connections, and you can see that we have 37 inbound connections
and 63 outbound connections. We've reached the limit and the resource manager
will block any further connections.

The next step in debugging is seeing if this is a recurring problem or just a
transient error. If it's a transient error it's okay to ignore it since the
resource manager was doing its job in keeping resource usage under the limit. If
it's recurring then you should understand what's causing you to hit these limits
and either refactor your application or raise the limits.

To check if it's a recurring problem you can count the number of times you've
seen the `"resource limit exceeded"` error over time. You can also check the
`rcmgr_blocked_resources` metric to see how many times the resource manager has
blocked a resource over time.

![Example graph of blocked resources over time](https://bafkreibul6qipnax5s42abv3jc6bolhd7pju3zbl4rcvdaklmk52f6cznu.ipfs.w3s.link/)

If the resource is blocked by a protocol-level scope, take a look at the various
resource usages in the metrics. For example, if you run into a new stream being blocked,
you can check the
`rcmgr_streams` metric and the "Streams by protocol" graph in the Grafana
dashboard (assuming you've set that up or something similar – see
[Monitoring](#monitoring)) to understand the usage pattern of that specific
protocol. This can help answer questions such as: "Am I constantly around my
limit?", "Does it make sense to raise my limit?", "Are there any patterns around
hitting this limit?", and "should I refactor my protocol implementation?"

## Monitoring

Once you have limits set, you'll want to monitor to see if you're running into
your limits often. This could be a sign that you need to raise your limits
(your process is more intensive than you originally thought) or that you need
to fix something in your application (surely you don't need over 1000 streams?).

There are OpenCensus metrics that can be hooked up to the resource manager. See
`obs/stats_test.go` for an example on how to enable this, and `DefaultViews` in
`stats.go` for recommended views. These metrics can be hooked up to Prometheus
or any other OpenCensus supported platform.

There is also an included Grafana dashboard to help kickstart your
observability into the resource manager. Find more information about it at
[here](./obs/grafana-dashboards/README.md).

## Allowlisting multiaddrs to mitigate eclipse attacks

If you have a set of trusted peers and IP addresses, you can use the resource
manager's [Allowlist](./docs/allowlist.md) to protect yourself from eclipse
attacks. The set of peers in the allowlist will have their own limits in case
the normal limits are reached. This means you will always be able to connect to
these trusted peers even if you've already reached your system limits.

Look at `WithAllowlistedMultiaddrs` and its example in the GoDoc to learn more.

## ConnManager vs Resource Manager

go-libp2p already includes a [connection
manager](https://pkg.go.dev/github.com/libp2p/go-libp2p-core/connmgr#ConnManager),
so what's the difference between the `ConnManager` and the `ResourceManager`?

ConnManager:
1. Configured with a low and high watermark number of connections.
2. Attempts to maintain the number of connections between the low and high
   markers.
3. Connections can be given metadata and weight (e.g. a hole punched
   connection is more valuable than a connection to a publicly addressable
   endpoint since it took more effort to make the hole punched connection).
4. The ConnManager will trim connections once the high watermark is reached. and
   trim down to the low watermark.
5. Won't block adding another connection above the high watermark, but will
   trigger the trim mentioned above.
6. Can trim and prioritize connections with custom logic.
7. No concept of scopes (like the resource manager).

Resource Manager:
1. Configured with limits on the number of outgoing and incoming connections at
   different [resource scopes](#resource-scopes).
2. Will block adding any more connections if any of the scope-specific limits would be exceeded.

The natural question when comparing these two managers is "how do the watermarks
and limits interact with each other?". The short answer is that they don't know
about each other. This can lead to some surprising subtleties, such as the
trimming never happening because the resource manager's limit is lower than the
high watermark. This is confusing, and we'd like to fix it. The issue is
captured in [go-libp2p#1640](https://github.com/libp2p/go-libp2p/issues/1640).

When configuring the resource manager and connection manager, you should set the
limits in the resource manager as your hard limits that you would never want to
go over, and set the low/high watermarks as the range at which your application
works best.

## Examples

Here we consider some concrete examples that can ellucidate the abstract
design as described so far.

### Stream Lifetime

Let's consider a stream and the limits that apply to it.
When the stream scope is first opened, it is created by calling
`ResourceManager.OpenStream`.

Initially the stream is constrained by:
- the system scope, where global hard limits apply.
- the transient scope, where unnegotiated streams live.
- the peer scope, where the limits for the peer at the other end of the stream
  apply.

Once the protocol has been negotiated, the protocol is set by calling
`StreamManagementScope.SetProtocol`. The constraint from the
transient scope is removed and the stream is now constrained by the
protocol instead.

More specifically, the following constraints apply:
- the system scope, where global hard limits apply.
- the peer scope, where the limits for the peer at the other end of the stream
  apply.
- the protocol scope, where the limits of the specific protocol used apply.

The existence of the protocol limit allows us to implicitly constrain
streams for services that have not been ported to the resource manager
yet.  Once the programmer attaches a stream to a service by calling
`StreamScope.SetService`, the stream resources are aggregated and constrained
by the service scope in addition to its protocol scope.

More specifically the following constraints apply:
- the system scope, where global hard limits apply.
- the peer scope, where the limits for the peer at the other end of the stream
  apply.
- the service scope, where the limits of the specific service owning the stream apply.
- the protcol scope, where the limits of the specific protocol for the stream apply.


The resource transfer that happens in the `SetProtocol` and `SetService`
gives the opportunity to the resource manager to gate the streams. If
the transfer results in exceeding the scope limits, then a error
indicating "resource limit exceeded" is returned. The wrapped error
includes the name of the scope rejecting the resource acquisition to
aid understanding of applicable limits.  Note that the (wrapped) error
implements `net.Error` and is marked as temporary, so that the
programmer can handle by backoff retry.


## Implementation Notes

- The package only exports a constructor for the resource manager and
  basic types for defining limits. Internals are not exposed.
- Internally, there is a resources object that is embedded in every scope and
  implements resource accounting.
- There is a single implementation of a generic resource scope, that
  provides all necessary interface methods.
- There are concrete types for all canonical scopes, embedding a
  pointer to a generic resource scope.
- Peer and Protocol scopes, which may be created in response to
  network events, are periodically garbage collected.

## Design Considerations

- The Resource Manager must account for basic resource usage at all
  levels of the stack, from the internals to application components
  that use the network facilities of libp2p.
- Basic resources include memory, streams, connections, and file
  descriptors. These account for both space and time used by
  the stack, as each resource has a direct effect on the system
  availability and performance.
- The design must support seamless integration for user applications,
  which should reap the benefits of resource management without any
  changes. That is, existing applications should be oblivious of the
  resource manager and transparently obtain limits which protects it
  from resource exhaustion and OOM conditions.
- At the same time, the design must support opt-in resource usage
  accounting for applications that want to explicitly utilize the
  facilities of the system to inform about and constrain their own
  resource usage.
- The design must allow the user to set their own limits, which can be
  static (fixed) or dynamic.
