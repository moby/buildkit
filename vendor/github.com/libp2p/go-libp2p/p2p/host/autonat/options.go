package autonat

import (
	"errors"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
)

// config holds configurable options for the autonat subsystem.
type config struct {
	host host.Host

	addressFunc       AddrFunc
	dialPolicy        dialPolicy
	dialer            network.Network
	forceReachability bool
	reachability      network.Reachability

	// client
	bootDelay          time.Duration
	retryInterval      time.Duration
	refreshInterval    time.Duration
	requestTimeout     time.Duration
	throttlePeerPeriod time.Duration

	// server
	dialTimeout         time.Duration
	maxPeerAddresses    int
	throttleGlobalMax   int
	throttlePeerMax     int
	throttleResetPeriod time.Duration
	throttleResetJitter time.Duration
}

var defaults = func(c *config) error {
	c.bootDelay = 15 * time.Second
	c.retryInterval = 90 * time.Second
	c.refreshInterval = 15 * time.Minute
	c.requestTimeout = 30 * time.Second
	c.throttlePeerPeriod = 90 * time.Second

	c.dialTimeout = 15 * time.Second
	c.maxPeerAddresses = 16
	c.throttleGlobalMax = 30
	c.throttlePeerMax = 3
	c.throttleResetPeriod = 1 * time.Minute
	c.throttleResetJitter = 15 * time.Second
	return nil
}

// EnableService specifies that AutoNAT should be allowed to run a NAT service to help
// other peers determine their own NAT status. The provided Network should not be the
// default network/dialer of the host passed to `New`, as the NAT system will need to
// make parallel connections, and as such will modify both the associated peerstore
// and terminate connections of this dialer. The dialer provided
// should be compatible (TCP/UDP) however with the transports of the libp2p network.
func EnableService(dialer network.Network) Option {
	return func(c *config) error {
		if dialer == c.host.Network() || dialer.Peerstore() == c.host.Peerstore() {
			return errors.New("dialer should not be that of the host")
		}
		c.dialer = dialer
		return nil
	}
}

// WithReachability overrides autonat to simply report an over-ridden reachability
// status.
func WithReachability(reachability network.Reachability) Option {
	return func(c *config) error {
		c.forceReachability = true
		c.reachability = reachability
		return nil
	}
}

// UsingAddresses allows overriding which Addresses the AutoNAT client believes
// are "its own". Useful for testing, or for more exotic port-forwarding
// scenarios where the host may be listening on different ports than it wants
// to externally advertise or verify connectability on.
func UsingAddresses(addrFunc AddrFunc) Option {
	return func(c *config) error {
		if addrFunc == nil {
			return errors.New("invalid address function supplied")
		}
		c.addressFunc = addrFunc
		return nil
	}
}

// WithSchedule configures how agressively probes will be made to verify the
// address of the host. retryInterval indicates how often probes should be made
// when the host lacks confident about its address, while refresh interval
// is the schedule of periodic probes when the host believes it knows its
// steady-state reachability.
func WithSchedule(retryInterval, refreshInterval time.Duration) Option {
	return func(c *config) error {
		c.retryInterval = retryInterval
		c.refreshInterval = refreshInterval
		return nil
	}
}

// WithoutStartupDelay removes the initial delay the NAT subsystem typically
// uses as a buffer for ensuring that connectivity and guesses as to the hosts
// local interfaces have settled down during startup.
func WithoutStartupDelay() Option {
	return func(c *config) error {
		c.bootDelay = 1
		return nil
	}
}

// WithoutThrottling indicates that this autonat service should not place
// restrictions on how many peers it is willing to help when acting as
// a server.
func WithoutThrottling() Option {
	return func(c *config) error {
		c.throttleGlobalMax = 0
		return nil
	}
}

// WithThrottling specifies how many peers (`amount`) it is willing to help
// ever `interval` amount of time when acting as a server.
func WithThrottling(amount int, interval time.Duration) Option {
	return func(c *config) error {
		c.throttleGlobalMax = amount
		c.throttleResetPeriod = interval
		c.throttleResetJitter = interval / 4
		return nil
	}
}

// WithPeerThrottling specifies a limit for the maximum number of IP checks
// this node will provide to an individual peer in each `interval`.
func WithPeerThrottling(amount int) Option {
	return func(c *config) error {
		c.throttlePeerMax = amount
		return nil
	}
}
