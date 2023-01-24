// Copyright 2012 Google, Inc. All rights reserved.
//
// Use of this source code is governed by a BSD-style license
// that can be found in the LICENSE file in the root of the source
// tree.

//go:build darwin || dragonfly || freebsd || netbsd || openbsd
// +build darwin dragonfly freebsd netbsd openbsd

// This is a BSD import for the routing structure initially found in
// https://github.com/google/gopacket/blob/master/routing/routing.go
// RIB parsing follows the BSD route format described in
// https://github.com/freebsd/freebsd/blob/master/sys/net/route.h
package netroute

import (
	"fmt"
	"net"
	"sort"
	"syscall"

	"github.com/google/gopacket/routing"
	"golang.org/x/net/route"
)

func toIPAddr(a route.Addr) (net.IP, error) {
	switch t := a.(type) {
	case *route.Inet4Addr:
		ip := net.IPv4(t.IP[0], t.IP[1], t.IP[2], t.IP[3])
		return ip, nil
	case *route.Inet6Addr:
		ip := make(net.IP, net.IPv6len)
		copy(ip, t.IP[:])
		return ip, nil
	default:
		return net.IP{}, fmt.Errorf("unknown family: %v", t)
	}
}

// selected BSD Route flags.
const (
	RTF_UP        = 0x1
	RTF_GATEWAY   = 0x2
	RTF_HOST      = 0x4
	RTF_REJECT    = 0x8
	RTF_DYNAMIC   = 0x10
	RTF_MODIFIED  = 0x20
	RTF_STATIC    = 0x800
	RTF_BLACKHOLE = 0x1000
	RTF_LOCAL     = 0x200000
	RTF_BROADCAST = 0x400000
	RTF_MULTICAST = 0x800000
)

func New() (routing.Router, error) {
	rtr := &router{}
	rtr.ifaces = make(map[int]net.Interface)
	rtr.addrs = make(map[int]ipAddrs)
	tab, err := route.FetchRIB(syscall.AF_UNSPEC, route.RIBTypeRoute, 0)
	if err != nil {
		return nil, err
	}
	msgs, err := route.ParseRIB(route.RIBTypeRoute, tab)
	if err != nil {
		return nil, err
	}
	var ipn *net.IPNet
	for _, msg := range msgs {
		m := msg.(*route.RouteMessage)
		// We ignore the error (m.Err) here. It's not clear what this error actually means,
		// and it makes us miss routes that _should_ be included.
		routeInfo := new(rtInfo)

		if m.Version < 3 || m.Version > 5 {
			return nil, fmt.Errorf("unexpected RIB message version: %d", m.Version)
		}
		if m.Type != 4 /* RTM_GET */ {
			return nil, fmt.Errorf("unexpected RIB message type: %d", m.Type)
		}

		if m.Flags&RTF_UP == 0 ||
			m.Flags&(RTF_REJECT|RTF_BLACKHOLE) != 0 {
			continue
		}

		dst, err := toIPAddr(m.Addrs[0])
		if err == nil {
			mask, _ := toIPAddr(m.Addrs[2])
			if mask == nil {
				mask = net.IP(net.CIDRMask(0, 8*len(dst)))
			}
			ipn = &net.IPNet{IP: dst, Mask: net.IPMask(mask)}
			if m.Flags&RTF_HOST != 0 {
				ipn.Mask = net.CIDRMask(8*len(ipn.IP), 8*len(ipn.IP))
			}
			routeInfo.Dst = ipn
		} else {
			return nil, fmt.Errorf("unexpected RIB destination: %v", err)
		}

		if m.Flags&RTF_GATEWAY != 0 {
			if gw, err := toIPAddr(m.Addrs[1]); err == nil {
				routeInfo.Gateway = gw
			}
		}
		if src, err := toIPAddr(m.Addrs[5]); err == nil {
			ipn = &net.IPNet{IP: src, Mask: net.CIDRMask(8*len(src), 8*len(src))}
			routeInfo.Src = ipn
			routeInfo.PrefSrc = src
			if m.Flags&0x2 != 0 /* RTF_GATEWAY */ {
				routeInfo.Src.Mask = net.CIDRMask(0, 8*len(routeInfo.Src.IP))
			}
		}
		routeInfo.OutputIface = uint32(m.Index)

		switch m.Addrs[0].(type) {
		case *route.Inet4Addr:
			rtr.v4 = append(rtr.v4, routeInfo)
		case *route.Inet6Addr:
			rtr.v6 = append(rtr.v6, routeInfo)
		}
	}
	sort.Sort(rtr.v4)
	sort.Sort(rtr.v6)
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	for _, iface := range ifaces {
		rtr.ifaces[iface.Index] = iface
		var addrs ipAddrs
		ifaceAddrs, err := iface.Addrs()
		if err != nil {
			return nil, err
		}
		for _, addr := range ifaceAddrs {
			if inet, ok := addr.(*net.IPNet); ok {
				// Go has a nasty habit of giving you IPv4s as ::ffff:1.2.3.4 instead of 1.2.3.4.
				// We want to use mapped v4 addresses as v4 preferred addresses, never as v6
				// preferred addresses.
				if v4 := inet.IP.To4(); v4 != nil {
					if addrs.v4 == nil {
						addrs.v4 = v4
					}
				} else if addrs.v6 == nil {
					addrs.v6 = inet.IP
				}
			}
		}
		rtr.addrs[iface.Index] = addrs
	}
	return rtr, nil
}
