// Package reuseport provides Listen and Dial functions that set socket
// options in order to be able to reuse ports. You should only use this
// package if you know what SO_REUSEADDR and SO_REUSEPORT are.
//
// For example:
//
//  // listen on the same port. oh yeah.
//  l1, _ := reuse.Listen("tcp", "127.0.0.1:1234")
//  l2, _ := reuse.Listen("tcp", "127.0.0.1:1234")
//
//  // dial from the same port. oh yeah.
//  l1, _ := reuse.Listen("tcp", "127.0.0.1:1234")
//  l2, _ := reuse.Listen("tcp", "127.0.0.1:1235")
//  c, _ := reuse.Dial("tcp", "127.0.0.1:1234", "127.0.0.1:1235")
//
// Note: cant dial self because tcp/ip stacks use 4-tuples to identify connections,
// and doing so would clash.
package reuseport

import (
	"context"
	"fmt"
	"net"
)

// Available returns whether or not SO_REUSEPORT or equivalent behaviour is
// available in the OS.
func Available() bool {
	return true
}

var listenConfig = net.ListenConfig{
	Control: Control,
}

// Listen listens at the given network and address. see net.Listen
// Returns a net.Listener created from a file discriptor for a socket
// with SO_REUSEPORT and SO_REUSEADDR option set.
func Listen(network, address string) (net.Listener, error) {
	return listenConfig.Listen(context.Background(), network, address)
}

// ListenPacket listens at the given network and address. see net.ListenPacket
// Returns a net.Listener created from a file discriptor for a socket
// with SO_REUSEPORT and SO_REUSEADDR option set.
func ListenPacket(network, address string) (net.PacketConn, error) {
	return listenConfig.ListenPacket(context.Background(), network, address)
}

// Dial dials the given network and address. see net.Dialer.Dial
// Returns a net.Conn created from a file descriptor for a socket
// with SO_REUSEPORT and SO_REUSEADDR option set.
func Dial(network, laddr, raddr string) (net.Conn, error) {
	nla, err := ResolveAddr(network, laddr)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve local addr: %w", err)
	}
	d := net.Dialer{
		Control:   Control,
		LocalAddr: nla,
	}
	return d.Dial(network, raddr)
}
