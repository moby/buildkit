// Package gostream allows to replace the standard net stack in Go
// with [LibP2P](https://github.com/libp2p/libp2p) streams.
//
// Given a libp2p.Host, gostream provides Dial() and Listen() methods which
// return implementations of net.Conn and net.Listener.
//
// Instead of the regular "host:port" addressing, `gostream` uses a Peer ID,
// and rather than a raw TCP connection, gostream will use libp2p's net.Stream.
// This means your connections will take advantage of  LibP2P's multi-routes,
// NAT transversal and stream multiplexing.
//
// Note that LibP2P hosts cannot dial to themselves, so there is no possibility
// of using the same Host as server and as client.
package gostream

// Network is the "net.Addr.Network()" name returned by
// addresses used by gostream connections. In turn, the "net.Addr.String()" will
// be a peer ID.
var Network = "libp2p"
