package client

import (
	"errors"
	"net"

	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

var _ manet.Listener = (*Listener)(nil)

type Listener Client

func (c *Client) Listener() *Listener {
	return (*Listener)(c)
}

func (l *Listener) Accept() (manet.Conn, error) {
	for {
		select {
		case evt := <-l.incoming:
			err := evt.writeResponse()
			if err != nil {
				log.Debugf("error writing relay response: %s", err.Error())
				evt.conn.stream.Reset()
				continue
			}

			log.Debugf("accepted relay connection from %s through %s", evt.conn.remote.ID, evt.conn.RemoteMultiaddr())

			evt.conn.tagHop()
			return evt.conn, nil

		case <-l.ctx.Done():
			return nil, errors.New("circuit v2 client closed")
		}
	}
}

func (l *Listener) Addr() net.Addr {
	return &NetAddr{
		Relay:  "any",
		Remote: "any",
	}
}

func (l *Listener) Multiaddr() ma.Multiaddr {
	return circuitAddr
}

func (l *Listener) Close() error {
	return (*Client)(l).Close()
}
