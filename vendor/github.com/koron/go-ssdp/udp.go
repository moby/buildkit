package ssdp

import (
	"net"
	"sync"
)

type packetHandler func(net.Addr, []byte) error

type udpAddrResolver struct {
	addr string

	mu  sync.RWMutex
	udp *net.UDPAddr
	err error
}

func (r *udpAddrResolver) setAddress(addr string) {
	r.mu.Lock()
	r.addr = addr
	r.udp = nil
	r.err = nil
	r.mu.Unlock()
}

func (r *udpAddrResolver) resolve() (*net.UDPAddr, error) {
	r.mu.RLock()
	if err := r.err; err != nil {
		r.mu.RUnlock()
		return nil, err
	}
	if udp := r.udp; udp != nil {
		r.mu.RUnlock()
		return udp, nil
	}
	r.mu.RUnlock()

	r.mu.Lock()
	defer r.mu.Unlock()
	r.udp, r.err = net.ResolveUDPAddr("udp4", r.addr)
	return r.udp, r.err
}

var recvAddrResolver = &udpAddrResolver{addr: "224.0.0.0:1900"}

// SetMulticastRecvAddrIPv4 updates multicast address where to receive packets.
// This never fail now.
func SetMulticastRecvAddrIPv4(addr string) error {
	recvAddrResolver.setAddress(addr)
	return nil
}

var sendAddrResolver = &udpAddrResolver{addr: "239.255.255.250:1900"}

// multicastSendAddr returns an address to send multicast UDP package.
func multicastSendAddr() (*net.UDPAddr, error) {
	return sendAddrResolver.resolve()
}

// SetMulticastSendAddrIPv4 updates a UDP address to send multicast packets.
// This never fail now.
func SetMulticastSendAddrIPv4(addr string) error {
	sendAddrResolver.setAddress(addr)
	return nil
}
