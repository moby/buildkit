// A stub routing table conformant interface for js/wasm environments.

//go:build js && wasm
// +build js,wasm

package netroute

import (
	"net"

	"github.com/google/gopacket/routing"
)

func New() (routing.Router, error) {
	rtr := &router{}
	rtr.ifaces = make(map[int]net.Interface)
	rtr.ifaces[0] = net.Interface{}
	rtr.addrs = make(map[int]ipAddrs)
	rtr.addrs[0] = ipAddrs{}
	rtr.v4 = routeSlice{&rtInfo{}}
	rtr.v6 = routeSlice{&rtInfo{}}
	return rtr, nil
}
