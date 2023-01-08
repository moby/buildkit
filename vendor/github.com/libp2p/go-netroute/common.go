// Copyright 2012 Google, Inc. All rights reserved.
//
// Use of this source code is governed by a BSD-style license
// that can be found in the LICENSE file in the root of the source
// tree.

// Originally found in
// https://github.com/google/gopacket/blob/master/routing/routing.go
// * Route selection modified to choose most selective route
//   to break ties when route priority is insufficient.
package netroute

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"strings"
)

// rtInfo contains information on a single route.
type rtInfo struct {
	Src, Dst         *net.IPNet
	Gateway, PrefSrc net.IP
	// We currently ignore the InputIface.
	InputIface, OutputIface uint32
	Priority                uint32
}

// routeSlice implements sort.Interface to sort routes by Priority.
type routeSlice []*rtInfo

func (r routeSlice) Len() int {
	return len(r)
}
func (r routeSlice) Less(i, j int) bool {
	return r[i].Priority < r[j].Priority
}
func (r routeSlice) Swap(i, j int) {
	r[i], r[j] = r[j], r[i]
}

type router struct {
	ifaces map[int]net.Interface
	addrs  map[int]ipAddrs
	v4, v6 routeSlice
}

func (r *router) String() string {
	strs := []string{"ROUTER", "--- V4 ---"}
	for _, route := range r.v4 {
		strs = append(strs, fmt.Sprintf("%+v", *route))
	}
	strs = append(strs, "--- V6 ---")
	for _, route := range r.v6 {
		strs = append(strs, fmt.Sprintf("%+v", *route))
	}
	return strings.Join(strs, "\n")
}

type ipAddrs struct {
	v4, v6 net.IP
}

func (r *router) Route(dst net.IP) (iface *net.Interface, gateway, preferredSrc net.IP, err error) {
	return r.RouteWithSrc(nil, nil, dst)
}

func (r *router) RouteWithSrc(input net.HardwareAddr, src, dst net.IP) (iface *net.Interface, gateway, preferredSrc net.IP, err error) {
	var ifaceIndex int
	switch {
	case dst.To4() != nil:
		ifaceIndex, gateway, preferredSrc, err = r.route(r.v4, input, src, dst)
	case dst.To16() != nil:
		ifaceIndex, gateway, preferredSrc, err = r.route(r.v6, input, src, dst)
	default:
		err = errors.New("IP is not valid as IPv4 or IPv6")
		return
	}
	if err != nil {
		return
	}

	// Interfaces are 1-indexed, but we store them in a 0-indexed array.
	correspondingIface, ok := r.ifaces[ifaceIndex]
	if !ok {
		err = errors.New("Route refereced unknown interface")
	}
	iface = &correspondingIface

	if preferredSrc == nil {
		switch {
		case dst.To4() != nil:
			preferredSrc = r.addrs[ifaceIndex].v4
		case dst.To16() != nil:
			preferredSrc = r.addrs[ifaceIndex].v6
		}
	}
	return
}

func (r *router) route(routes routeSlice, input net.HardwareAddr, src, dst net.IP) (iface int, gateway, preferredSrc net.IP, err error) {
	var inputIndex uint32
	if input != nil {
		for i, iface := range r.ifaces {
			if bytes.Equal(input, iface.HardwareAddr) {
				// Convert from zero- to one-indexed.
				inputIndex = uint32(i + 1)
				break
			}
		}
	}
	var mostSpecificRt *rtInfo
	for _, rt := range routes {
		if rt.InputIface != 0 && rt.InputIface != inputIndex {
			continue
		}
		if src != nil && rt.Src != nil && !rt.Src.Contains(src) {
			continue
		}
		if rt.Dst != nil && !rt.Dst.Contains(dst) {
			continue
		}
		if mostSpecificRt != nil {
			var candSpec, curSpec int
			if rt.Dst != nil {
				candSpec, _ = rt.Dst.Mask.Size()
			}
			if mostSpecificRt.Dst != nil {
				curSpec, _ = mostSpecificRt.Dst.Mask.Size()
			}
			if candSpec < curSpec {
				continue
			}
		}
		mostSpecificRt = rt
	}
	if mostSpecificRt != nil {
		return int(mostSpecificRt.OutputIface), mostSpecificRt.Gateway, mostSpecificRt.PrefSrc, nil
	}
	err = fmt.Errorf("no route found for %v", dst)
	return
}
