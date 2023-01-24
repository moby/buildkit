package upgrader

import (
	"context"
	"fmt"
	"sync"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/transport"

	logging "github.com/ipfs/go-log/v2"
	tec "github.com/jbenet/go-temp-err-catcher"
	manet "github.com/multiformats/go-multiaddr/net"
)

var log = logging.Logger("upgrader")

type listener struct {
	manet.Listener

	transport transport.Transport
	upgrader  *upgrader
	rcmgr     network.ResourceManager

	incoming chan transport.CapableConn
	err      error

	// Used for backpressure
	threshold *threshold

	// Canceling this context isn't sufficient to tear down the listener.
	// Call close.
	ctx    context.Context
	cancel func()
}

// Close closes the listener.
func (l *listener) Close() error {
	// Do this first to try to get any relevent errors.
	err := l.Listener.Close()

	l.cancel()
	// Drain and wait.
	for c := range l.incoming {
		c.Close()
	}
	return err
}

// handles inbound connections.
//
// This function does a few interesting things that should be noted:
//
//  1. It logs and discards temporary/transient errors (errors with a Temporary()
//     function that returns true).
//  2. It stops accepting new connections once AcceptQueueLength connections have
//     been fully negotiated but not accepted. This gives us a basic backpressure
//     mechanism while still allowing us to negotiate connections in parallel.
func (l *listener) handleIncoming() {
	var wg sync.WaitGroup
	defer func() {
		// make sure we're closed
		l.Listener.Close()
		if l.err == nil {
			l.err = fmt.Errorf("listener closed")
		}

		wg.Wait()
		close(l.incoming)
	}()

	var catcher tec.TempErrCatcher
	for l.ctx.Err() == nil {
		maconn, err := l.Listener.Accept()
		if err != nil {
			// Note: function may pause the accept loop.
			if catcher.IsTemporary(err) {
				log.Infof("temporary accept error: %s", err)
				continue
			}
			l.err = err
			return
		}
		catcher.Reset()

		// gate the connection if applicable
		if l.upgrader.connGater != nil && !l.upgrader.connGater.InterceptAccept(maconn) {
			log.Debugf("gater blocked incoming connection on local addr %s from %s",
				maconn.LocalMultiaddr(), maconn.RemoteMultiaddr())
			if err := maconn.Close(); err != nil {
				log.Warnf("failed to close incoming connection rejected by gater: %s", err)
			}
			continue
		}

		connScope, err := l.rcmgr.OpenConnection(network.DirInbound, true, maconn.RemoteMultiaddr())
		if err != nil {
			log.Debugw("resource manager blocked accept of new connection", "error", err)
			if err := maconn.Close(); err != nil {
				log.Warnf("failed to incoming connection rejected by resource manager: %s", err)
			}
			continue
		}

		// The go routine below calls Release when the context is
		// canceled so there's no need to wait on it here.
		l.threshold.Wait()

		log.Debugf("listener %s got connection: %s <---> %s",
			l,
			maconn.LocalMultiaddr(),
			maconn.RemoteMultiaddr())

		wg.Add(1)
		go func() {
			defer wg.Done()

			ctx, cancel := context.WithTimeout(l.ctx, l.upgrader.acceptTimeout)
			defer cancel()

			conn, err := l.upgrader.Upgrade(ctx, l.transport, maconn, network.DirInbound, "", connScope)
			if err != nil {
				// Don't bother bubbling this up. We just failed
				// to completely negotiate the connection.
				log.Debugf("accept upgrade error: %s (%s <--> %s)",
					err,
					maconn.LocalMultiaddr(),
					maconn.RemoteMultiaddr())
				connScope.Done()
				return
			}

			log.Debugf("listener %s accepted connection: %s", l, conn)

			// This records the fact that the connection has been
			// setup and is waiting to be accepted. This call
			// *never* blocks, even if we go over the threshold. It
			// simply ensures that calls to Wait block while we're
			// over the threshold.
			l.threshold.Acquire()
			defer l.threshold.Release()

			select {
			case l.incoming <- conn:
			case <-ctx.Done():
				if l.ctx.Err() == nil {
					// Listener *not* closed but the accept timeout expired.
					log.Warn("listener dropped connection due to slow accept")
				}
				// Wait on the context with a timeout. This way,
				// if we stop accepting connections for some reason,
				// we'll eventually close all the open ones
				// instead of hanging onto them.
				conn.Close()
			}
		}()
	}
}

// Accept accepts a connection.
func (l *listener) Accept() (transport.CapableConn, error) {
	for c := range l.incoming {
		// Could have been sitting there for a while.
		if !c.IsClosed() {
			return c, nil
		}
	}
	return nil, l.err
}

func (l *listener) String() string {
	if s, ok := l.transport.(fmt.Stringer); ok {
		return fmt.Sprintf("<stream.Listener[%s] %s>", s, l.Multiaddr())
	}
	return fmt.Sprintf("<stream.Listener %s>", l.Multiaddr())
}

var _ transport.Listener = (*listener)(nil)
