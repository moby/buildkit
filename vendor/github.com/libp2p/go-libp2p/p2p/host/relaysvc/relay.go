package relaysvc

import (
	"context"
	"sync"

	relayv2 "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"

	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
)

type RelayManager struct {
	host host.Host

	mutex sync.Mutex
	relay *relayv2.Relay
	opts  []relayv2.Option

	refCount  sync.WaitGroup
	ctxCancel context.CancelFunc
}

func NewRelayManager(host host.Host, opts ...relayv2.Option) *RelayManager {
	ctx, cancel := context.WithCancel(context.Background())
	m := &RelayManager{
		host:      host,
		opts:      opts,
		ctxCancel: cancel,
	}
	m.refCount.Add(1)
	go m.background(ctx)
	return m
}

func (m *RelayManager) background(ctx context.Context) {
	defer m.refCount.Done()
	defer func() {
		m.mutex.Lock()
		if m.relay != nil {
			m.relay.Close()
		}
		m.mutex.Unlock()
	}()

	subReachability, _ := m.host.EventBus().Subscribe(new(event.EvtLocalReachabilityChanged))
	defer subReachability.Close()

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-subReachability.Out():
			if !ok {
				return
			}
			if err := m.reachabilityChanged(ev.(event.EvtLocalReachabilityChanged).Reachability); err != nil {
				return
			}
		}
	}
}

func (m *RelayManager) reachabilityChanged(r network.Reachability) error {
	switch r {
	case network.ReachabilityPublic:
		relay, err := relayv2.New(m.host, m.opts...)
		if err != nil {
			return err
		}
		m.mutex.Lock()
		defer m.mutex.Unlock()
		m.relay = relay
	case network.ReachabilityPrivate:
		m.mutex.Lock()
		defer m.mutex.Unlock()
		if m.relay != nil {
			err := m.relay.Close()
			m.relay = nil
			return err
		}
	}
	return nil
}

func (m *RelayManager) Close() error {
	m.ctxCancel()
	m.refCount.Wait()
	return nil
}
