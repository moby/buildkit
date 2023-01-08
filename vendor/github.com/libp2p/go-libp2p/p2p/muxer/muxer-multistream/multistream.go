// Package muxer_multistream implements a peerstream transport using
// go-multistream to select the underlying stream muxer
package muxer_multistream

import (
	"fmt"
	"net"
	"time"

	"github.com/libp2p/go-libp2p/core/network"

	mss "github.com/multiformats/go-multistream"
)

var DefaultNegotiateTimeout = time.Second * 60

type Transport struct {
	mux *mss.MultistreamMuxer

	tpts map[string]network.Multiplexer

	NegotiateTimeout time.Duration

	OrderPreference []string
}

func NewBlankTransport() *Transport {
	return &Transport{
		mux:              mss.NewMultistreamMuxer(),
		tpts:             make(map[string]network.Multiplexer),
		NegotiateTimeout: DefaultNegotiateTimeout,
	}
}

func (t *Transport) AddTransport(path string, tpt network.Multiplexer) {
	t.mux.AddHandler(path, nil)
	t.tpts[path] = tpt
	t.OrderPreference = append(t.OrderPreference, path)
}

func (t *Transport) NewConn(nc net.Conn, isServer bool, scope network.PeerScope) (network.MuxedConn, error) {
	if t.NegotiateTimeout != 0 {
		if err := nc.SetDeadline(time.Now().Add(t.NegotiateTimeout)); err != nil {
			return nil, err
		}
	}

	var proto string
	if isServer {
		selected, _, err := t.mux.Negotiate(nc)
		if err != nil {
			return nil, err
		}
		proto = selected
	} else {
		selected, err := mss.SelectOneOf(t.OrderPreference, nc)
		if err != nil {
			return nil, err
		}
		proto = selected
	}

	if t.NegotiateTimeout != 0 {
		if err := nc.SetDeadline(time.Time{}); err != nil {
			return nil, err
		}
	}

	tpt, ok := t.tpts[proto]
	if !ok {
		return nil, fmt.Errorf("selected protocol we don't have a transport for")
	}

	return tpt.NewConn(nc, isServer, scope)
}
