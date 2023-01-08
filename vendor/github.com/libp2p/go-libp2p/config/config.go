package config

import (
	"crypto/rand"
	"fmt"
	"time"

	"github.com/libp2p/go-libp2p/core/connmgr"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/metrics"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/core/pnet"
	"github.com/libp2p/go-libp2p/core/routing"
	"github.com/libp2p/go-libp2p/core/sec"
	"github.com/libp2p/go-libp2p/core/transport"
	"github.com/libp2p/go-libp2p/p2p/host/autonat"
	"github.com/libp2p/go-libp2p/p2p/host/autorelay"
	bhost "github.com/libp2p/go-libp2p/p2p/host/basic"
	blankhost "github.com/libp2p/go-libp2p/p2p/host/blank"
	"github.com/libp2p/go-libp2p/p2p/host/peerstore/pstoremem"
	routed "github.com/libp2p/go-libp2p/p2p/host/routed"
	"github.com/libp2p/go-libp2p/p2p/net/swarm"
	tptu "github.com/libp2p/go-libp2p/p2p/net/upgrader"
	circuitv2 "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/client"
	relayv2 "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"
	"github.com/libp2p/go-libp2p/p2p/protocol/holepunch"

	logging "github.com/ipfs/go-log/v2"
	ma "github.com/multiformats/go-multiaddr"
	madns "github.com/multiformats/go-multiaddr-dns"
)

var log = logging.Logger("p2p-config")

// AddrsFactory is a function that takes a set of multiaddrs we're listening on and
// returns the set of multiaddrs we should advertise to the network.
type AddrsFactory = bhost.AddrsFactory

// NATManagerC is a NATManager constructor.
type NATManagerC func(network.Network) bhost.NATManager

type RoutingC func(host.Host) (routing.PeerRouting, error)

// AutoNATConfig defines the AutoNAT behavior for the libp2p host.
type AutoNATConfig struct {
	ForceReachability   *network.Reachability
	EnableService       bool
	ThrottleGlobalLimit int
	ThrottlePeerLimit   int
	ThrottleInterval    time.Duration
}

// Config describes a set of settings for a libp2p node
//
// This is *not* a stable interface. Use the options defined in the root
// package.
type Config struct {
	// UserAgent is the identifier this node will send to other peers when
	// identifying itself, e.g. via the identify protocol.
	//
	// Set it via the UserAgent option function.
	UserAgent string

	PeerKey crypto.PrivKey

	Transports         []TptC
	Muxers             []MsMuxC
	SecurityTransports []MsSecC
	Insecure           bool
	PSK                pnet.PSK

	DialTimeout time.Duration

	RelayCustom bool
	Relay       bool // should the relay transport be used

	EnableRelayService bool // should we run a circuitv2 relay (if publicly reachable)
	RelayServiceOpts   []relayv2.Option

	ListenAddrs     []ma.Multiaddr
	AddrsFactory    bhost.AddrsFactory
	ConnectionGater connmgr.ConnectionGater

	ConnManager     connmgr.ConnManager
	ResourceManager network.ResourceManager

	NATManager NATManagerC
	Peerstore  peerstore.Peerstore
	Reporter   metrics.Reporter

	MultiaddrResolver *madns.Resolver

	DisablePing bool

	Routing RoutingC

	EnableAutoRelay bool
	AutoRelayOpts   []autorelay.Option
	AutoNATConfig

	EnableHolePunching  bool
	HolePunchingOptions []holepunch.Option
}

func (cfg *Config) makeSwarm() (*swarm.Swarm, error) {
	if cfg.Peerstore == nil {
		return nil, fmt.Errorf("no peerstore specified")
	}

	// Check this early. Prevents us from even *starting* without verifying this.
	if pnet.ForcePrivateNetwork && len(cfg.PSK) == 0 {
		log.Error("tried to create a libp2p node with no Private" +
			" Network Protector but usage of Private Networks" +
			" is forced by the enviroment")
		// Note: This is *also* checked the upgrader itself so it'll be
		// enforced even *if* you don't use the libp2p constructor.
		return nil, pnet.ErrNotInPrivateNetwork
	}

	if cfg.PeerKey == nil {
		return nil, fmt.Errorf("no peer key specified")
	}

	// Obtain Peer ID from public key
	pid, err := peer.IDFromPublicKey(cfg.PeerKey.GetPublic())
	if err != nil {
		return nil, err
	}

	if err := cfg.Peerstore.AddPrivKey(pid, cfg.PeerKey); err != nil {
		return nil, err
	}
	if err := cfg.Peerstore.AddPubKey(pid, cfg.PeerKey.GetPublic()); err != nil {
		return nil, err
	}

	opts := make([]swarm.Option, 0, 3)
	if cfg.Reporter != nil {
		opts = append(opts, swarm.WithMetrics(cfg.Reporter))
	}
	if cfg.ConnectionGater != nil {
		opts = append(opts, swarm.WithConnectionGater(cfg.ConnectionGater))
	}
	if cfg.DialTimeout != 0 {
		opts = append(opts, swarm.WithDialTimeout(cfg.DialTimeout))
	}
	if cfg.ResourceManager != nil {
		opts = append(opts, swarm.WithResourceManager(cfg.ResourceManager))
	}
	// TODO: Make the swarm implementation configurable.
	return swarm.NewSwarm(pid, cfg.Peerstore, opts...)
}

func (cfg *Config) addTransports(h host.Host) error {
	swrm, ok := h.Network().(transport.TransportNetwork)
	if !ok {
		// Should probably skip this if no transports.
		return fmt.Errorf("swarm does not support transports")
	}
	var secure sec.SecureMuxer
	if cfg.Insecure {
		secure = makeInsecureTransport(h.ID(), cfg.PeerKey)
	} else {
		var err error
		secure, err = makeSecurityMuxer(h, cfg.SecurityTransports)
		if err != nil {
			return err
		}
	}
	muxer, err := makeMuxer(h, cfg.Muxers)
	if err != nil {
		return err
	}
	var opts []tptu.Option
	if len(cfg.PSK) > 0 {
		opts = append(opts, tptu.WithPSK(cfg.PSK))
	}
	if cfg.ConnectionGater != nil {
		opts = append(opts, tptu.WithConnectionGater(cfg.ConnectionGater))
	}
	if cfg.ResourceManager != nil {
		opts = append(opts, tptu.WithResourceManager(cfg.ResourceManager))
	}
	upgrader, err := tptu.New(secure, muxer, opts...)
	if err != nil {
		return err
	}
	tpts, err := makeTransports(h, upgrader, cfg.ConnectionGater, cfg.PSK, cfg.ResourceManager, cfg.Transports)
	if err != nil {
		return err
	}
	for _, t := range tpts {
		if err := swrm.AddTransport(t); err != nil {
			return err
		}
	}

	if cfg.Relay {
		if err := circuitv2.AddTransport(h, upgrader); err != nil {
			h.Close()
			return err
		}
	}

	return nil
}

// NewNode constructs a new libp2p Host from the Config.
//
// This function consumes the config. Do not reuse it (really!).
func (cfg *Config) NewNode() (host.Host, error) {
	swrm, err := cfg.makeSwarm()
	if err != nil {
		return nil, err
	}

	h, err := bhost.NewHost(swrm, &bhost.HostOpts{
		ConnManager:         cfg.ConnManager,
		AddrsFactory:        cfg.AddrsFactory,
		NATManager:          cfg.NATManager,
		EnablePing:          !cfg.DisablePing,
		UserAgent:           cfg.UserAgent,
		MultiaddrResolver:   cfg.MultiaddrResolver,
		EnableHolePunching:  cfg.EnableHolePunching,
		HolePunchingOptions: cfg.HolePunchingOptions,
		EnableRelayService:  cfg.EnableRelayService,
		RelayServiceOpts:    cfg.RelayServiceOpts,
	})
	if err != nil {
		swrm.Close()
		return nil, err
	}

	if cfg.Relay {
		// If we've enabled the relay, we should filter out relay
		// addresses by default.
		//
		// TODO: We shouldn't be doing this here.
		oldFactory := h.AddrsFactory
		h.AddrsFactory = func(addrs []ma.Multiaddr) []ma.Multiaddr {
			return oldFactory(autorelay.Filter(addrs))
		}
	}

	if err := cfg.addTransports(h); err != nil {
		h.Close()
		return nil, err
	}

	// TODO: This method succeeds if listening on one address succeeds. We
	// should probably fail if listening on *any* addr fails.
	if err := h.Network().Listen(cfg.ListenAddrs...); err != nil {
		h.Close()
		return nil, err
	}

	// Configure routing and autorelay
	var router routing.PeerRouting
	if cfg.Routing != nil {
		router, err = cfg.Routing(h)
		if err != nil {
			h.Close()
			return nil, err
		}
	}

	// Note: h.AddrsFactory may be changed by relayFinder, but non-relay version is
	// used by AutoNAT below.
	var ar *autorelay.AutoRelay
	addrF := h.AddrsFactory
	if cfg.EnableAutoRelay {
		if !cfg.Relay {
			h.Close()
			return nil, fmt.Errorf("cannot enable autorelay; relay is not enabled")
		}

		ar, err = autorelay.NewAutoRelay(h, cfg.AutoRelayOpts...)
		if err != nil {
			return nil, err
		}
	}

	autonatOpts := []autonat.Option{
		autonat.UsingAddresses(func() []ma.Multiaddr {
			return addrF(h.AllAddrs())
		}),
	}
	if cfg.AutoNATConfig.ThrottleInterval != 0 {
		autonatOpts = append(autonatOpts,
			autonat.WithThrottling(cfg.AutoNATConfig.ThrottleGlobalLimit, cfg.AutoNATConfig.ThrottleInterval),
			autonat.WithPeerThrottling(cfg.AutoNATConfig.ThrottlePeerLimit))
	}
	if cfg.AutoNATConfig.EnableService {
		autonatPrivKey, _, err := crypto.GenerateEd25519Key(rand.Reader)
		if err != nil {
			return nil, err
		}
		ps, err := pstoremem.NewPeerstore()
		if err != nil {
			return nil, err
		}

		// Pull out the pieces of the config that we _actually_ care about.
		// Specifically, don't setup things like autorelay, listeners,
		// identify, etc.
		autoNatCfg := Config{
			Transports:         cfg.Transports,
			Muxers:             cfg.Muxers,
			SecurityTransports: cfg.SecurityTransports,
			Insecure:           cfg.Insecure,
			PSK:                cfg.PSK,
			ConnectionGater:    cfg.ConnectionGater,
			Reporter:           cfg.Reporter,
			PeerKey:            autonatPrivKey,
			Peerstore:          ps,
		}

		dialer, err := autoNatCfg.makeSwarm()
		if err != nil {
			h.Close()
			return nil, err
		}
		dialerHost := blankhost.NewBlankHost(dialer)
		if err := autoNatCfg.addTransports(dialerHost); err != nil {
			dialerHost.Close()
			h.Close()
			return nil, err
		}
		// NOTE: We're dropping the blank host here but that's fine. It
		// doesn't really _do_ anything and doesn't even need to be
		// closed (as long as we close the underlying network).
		autonatOpts = append(autonatOpts, autonat.EnableService(dialerHost.Network()))
	}
	if cfg.AutoNATConfig.ForceReachability != nil {
		autonatOpts = append(autonatOpts, autonat.WithReachability(*cfg.AutoNATConfig.ForceReachability))
	}

	autonat, err := autonat.New(h, autonatOpts...)
	if err != nil {
		h.Close()
		return nil, fmt.Errorf("cannot enable autorelay; autonat failed to start: %v", err)
	}
	h.SetAutoNat(autonat)

	// start the host background tasks
	h.Start()

	var ho host.Host
	ho = h
	if router != nil {
		ho = routed.Wrap(h, router)
	}
	if ar != nil {
		return autorelay.NewAutoRelayHost(ho, ar), nil
	}
	return ho, nil
}

// Option is a libp2p config option that can be given to the libp2p constructor
// (`libp2p.New`).
type Option func(cfg *Config) error

// Apply applies the given options to the config, returning the first error
// encountered (if any).
func (cfg *Config) Apply(opts ...Option) error {
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		if err := opt(cfg); err != nil {
			return err
		}
	}
	return nil
}
