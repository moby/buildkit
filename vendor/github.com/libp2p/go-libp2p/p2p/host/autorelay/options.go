package autorelay

import (
	"errors"
	"fmt"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/benbjohnson/clock"
)

type config struct {
	clock      clock.Clock
	peerSource func(num int) <-chan peer.AddrInfo
	// minimum interval used to call the peerSource callback
	minInterval  time.Duration
	staticRelays []peer.AddrInfo
	// see WithMinCandidates
	minCandidates int
	// see WithMaxCandidates
	maxCandidates int
	// Delay until we obtain reservations with relays, if we have less than minCandidates candidates.
	// See WithBootDelay.
	bootDelay time.Duration
	// backoff is the time we wait after failing to obtain a reservation with a candidate
	backoff time.Duration
	// Number of relays we strive to obtain a reservation with.
	desiredRelays int
	// see WithMaxCandidateAge
	maxCandidateAge  time.Duration
	setMinCandidates bool
	enableCircuitV1  bool
}

var defaultConfig = config{
	clock:           clock.New(),
	minCandidates:   4,
	maxCandidates:   20,
	bootDelay:       3 * time.Minute,
	backoff:         time.Hour,
	desiredRelays:   2,
	maxCandidateAge: 30 * time.Minute,
}

var (
	errStaticRelaysMinCandidates = errors.New("cannot use WithMinCandidates and WithStaticRelays")
	errStaticRelaysPeerSource    = errors.New("cannot use WithPeerSource and WithStaticRelays")
)

// DefaultRelays are the known PL-operated v1 relays; will be decommissioned in 2022.
var DefaultRelays = []string{
	"/ip4/147.75.80.110/tcp/4001/p2p/QmbFgm5zan8P6eWWmeyfncR5feYEMPbht5b1FW1C37aQ7y",
	"/ip4/147.75.80.110/udp/4001/quic/p2p/QmbFgm5zan8P6eWWmeyfncR5feYEMPbht5b1FW1C37aQ7y",
	"/ip4/147.75.195.153/tcp/4001/p2p/QmW9m57aiBDHAkKj9nmFSEn7ZqrcF1fZS4bipsTCHburei",
	"/ip4/147.75.195.153/udp/4001/quic/p2p/QmW9m57aiBDHAkKj9nmFSEn7ZqrcF1fZS4bipsTCHburei",
	"/ip4/147.75.70.221/tcp/4001/p2p/Qme8g49gm3q4Acp7xWBKg3nAa9fxZ1YmyDJdyGgoG6LsXh",
	"/ip4/147.75.70.221/udp/4001/quic/p2p/Qme8g49gm3q4Acp7xWBKg3nAa9fxZ1YmyDJdyGgoG6LsXh",
}

var defaultStaticRelays []peer.AddrInfo

func init() {
	for _, s := range DefaultRelays {
		pi, err := peer.AddrInfoFromString(s)
		if err != nil {
			panic(fmt.Sprintf("failed to initialize default static relays: %s", err))
		}
		defaultStaticRelays = append(defaultStaticRelays, *pi)
	}
}

type Option func(*config) error

func WithStaticRelays(static []peer.AddrInfo) Option {
	return func(c *config) error {
		if c.setMinCandidates {
			return errStaticRelaysMinCandidates
		}
		if c.peerSource != nil {
			return errStaticRelaysPeerSource
		}
		if len(c.staticRelays) > 0 {
			return errors.New("can't set static relays, static relays already configured")
		}
		c.minCandidates = len(static)
		c.staticRelays = static
		return nil
	}
}

func WithDefaultStaticRelays() Option {
	return WithStaticRelays(defaultStaticRelays)
}

// WithPeerSource defines a callback for AutoRelay to query for more relay candidates.
// AutoRelay will call this function when it needs new candidates is connected to the desired number of
// relays, and it has enough candidates (in case we get disconnected from one of the relays).
// Implementations must send *at most* numPeers, and close the channel when they don't intend to provide
// any more peers.
// AutoRelay will not call the callback again until the channel is closed.
// Implementations should send new peers, but may send peers they sent before. AutoRelay implements
// a per-peer backoff (see WithBackoff).
// minInterval is the minimum interval this callback is called with, even if AutoRelay needs new candidates.
func WithPeerSource(f func(numPeers int) <-chan peer.AddrInfo, minInterval time.Duration) Option {
	return func(c *config) error {
		if len(c.staticRelays) > 0 {
			return errStaticRelaysPeerSource
		}
		c.peerSource = f
		c.minInterval = minInterval
		return nil
	}
}

// WithNumRelays sets the number of relays we strive to obtain reservations with.
func WithNumRelays(n int) Option {
	return func(c *config) error {
		c.desiredRelays = n
		return nil
	}
}

// WithMaxCandidates sets the number of relay candidates that we buffer.
func WithMaxCandidates(n int) Option {
	return func(c *config) error {
		c.maxCandidates = n
		if c.minCandidates > n {
			c.minCandidates = n
		}
		return nil
	}
}

// WithMinCandidates sets the minimum number of relay candidates we collect before to get a reservation
// with any of them (unless we've been running for longer than the boot delay).
// This is to make sure that we don't just randomly connect to the first candidate that we discover.
func WithMinCandidates(n int) Option {
	return func(c *config) error {
		if len(c.staticRelays) > 0 {
			return errStaticRelaysMinCandidates
		}
		if n > c.maxCandidates {
			n = c.maxCandidates
		}
		c.minCandidates = n
		c.setMinCandidates = true
		return nil
	}
}

// WithBootDelay set the boot delay for finding relays.
// We won't attempt any reservation if we've have less than a minimum number of candidates.
// This prevents us to connect to the "first best" relay, and allows us to carefully select the relay.
// However, in case we haven't found enough relays after the boot delay, we use what we have.
func WithBootDelay(d time.Duration) Option {
	return func(c *config) error {
		c.bootDelay = d
		return nil
	}
}

// WithBackoff sets the time we wait after failing to obtain a reservation with a candidate.
func WithBackoff(d time.Duration) Option {
	return func(c *config) error {
		c.backoff = d
		return nil
	}
}

// WithCircuitV1Support enables support for circuit v1 relays.
func WithCircuitV1Support() Option {
	return func(c *config) error {
		c.enableCircuitV1 = true
		return nil
	}
}

// WithMaxCandidateAge sets the maximum age of a candidate.
// When we are connected to the desired number of relays, we don't ask the peer source for new candidates.
// This can lead to AutoRelay's candidate list becoming outdated, and means we won't be able
// to quickly establish a new relay connection if our existing connection breaks, if all the candidates
// have become stale.
func WithMaxCandidateAge(d time.Duration) Option {
	return func(c *config) error {
		c.maxCandidateAge = d
		return nil
	}
}

func WithClock(cl clock.Clock) Option {
	return func(c *config) error {
		c.clock = cl
		return nil
	}
}
