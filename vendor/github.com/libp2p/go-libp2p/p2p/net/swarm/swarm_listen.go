package swarm

import (
	"fmt"
	"time"

	"github.com/libp2p/go-libp2p/core/canonicallog"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/transport"

	ma "github.com/multiformats/go-multiaddr"
)

// Listen sets up listeners for all of the given addresses.
// It returns as long as we successfully listen on at least *one* address.
func (s *Swarm) Listen(addrs ...ma.Multiaddr) error {
	errs := make([]error, len(addrs))
	var succeeded int
	for i, a := range addrs {
		if err := s.AddListenAddr(a); err != nil {
			errs[i] = err
		} else {
			succeeded++
		}
	}

	for i, e := range errs {
		if e != nil {
			log.Warnw("listening failed", "on", addrs[i], "error", errs[i])
		}
	}

	if succeeded == 0 && len(addrs) > 0 {
		return fmt.Errorf("failed to listen on any addresses: %s", errs)
	}

	return nil
}

// ListenClose stop and delete listeners for all of the given addresses.
func (s *Swarm) ListenClose(addrs ...ma.Multiaddr) {
	var listenersToClose []transport.Listener

	s.listeners.Lock()
	for l := range s.listeners.m {
		if !containsMultiaddr(addrs, l.Multiaddr()) {
			continue
		}

		delete(s.listeners.m, l)
		listenersToClose = append(listenersToClose, l)
	}
	s.listeners.cacheEOL = time.Time{}
	s.listeners.Unlock()

	for _, l := range listenersToClose {
		l.Close()
	}
}

// AddListenAddr tells the swarm to listen on a single address. Unlike Listen,
// this method does not attempt to filter out bad addresses.
func (s *Swarm) AddListenAddr(a ma.Multiaddr) error {
	tpt := s.TransportForListening(a)
	if tpt == nil {
		// TransportForListening will return nil if either:
		// 1. No transport has been registered.
		// 2. We're closed (so we've nulled out the transport map.
		//
		// Distinguish between these two cases to avoid confusing users.
		select {
		case <-s.ctx.Done():
			return ErrSwarmClosed
		default:
			return ErrNoTransport
		}
	}

	list, err := tpt.Listen(a)
	if err != nil {
		return err
	}

	s.listeners.Lock()
	if s.listeners.m == nil {
		s.listeners.Unlock()
		list.Close()
		return ErrSwarmClosed
	}
	s.refs.Add(1)
	s.listeners.m[list] = struct{}{}
	s.listeners.cacheEOL = time.Time{}
	s.listeners.Unlock()

	maddr := list.Multiaddr()

	// signal to our notifiees on listen.
	s.notifyAll(func(n network.Notifiee) {
		n.Listen(s, maddr)
	})

	go func() {
		defer func() {
			s.listeners.Lock()
			_, ok := s.listeners.m[list]
			if ok {
				delete(s.listeners.m, list)
				s.listeners.cacheEOL = time.Time{}
			}
			s.listeners.Unlock()

			if ok {
				list.Close()
				log.Errorf("swarm listener unintentionally closed")
			}

			// signal to our notifiees on listen close.
			s.notifyAll(func(n network.Notifiee) {
				n.ListenClose(s, maddr)
			})
			s.refs.Done()
		}()
		for {
			c, err := list.Accept()
			if err != nil {
				return
			}
			canonicallog.LogPeerStatus(100, c.RemotePeer(), c.RemoteMultiaddr(), "connection_status", "established", "dir", "inbound")

			log.Debugf("swarm listener accepted connection: %s <-> %s", c.LocalMultiaddr(), c.RemoteMultiaddr())
			s.refs.Add(1)
			go func() {
				defer s.refs.Done()
				_, err := s.addConn(c, network.DirInbound)
				switch err {
				case nil:
				case ErrSwarmClosed:
					// ignore.
					return
				default:
					log.Warnw("adding connection failed", "to", a, "error", err)
					return
				}
			}()
		}
	}()
	return nil
}

func containsMultiaddr(addrs []ma.Multiaddr, addr ma.Multiaddr) bool {
	for _, a := range addrs {
		if addr == a {
			return true
		}
	}
	return false
}
