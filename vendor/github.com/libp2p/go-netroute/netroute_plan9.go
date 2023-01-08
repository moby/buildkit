// Generate a local routing table structure following the code at
// https://github.com/google/gopacket/blob/master/routing/routing.go
//
// Plan 9 networking is described here: http://9p.io/magic/man2html/3/ip

package netroute

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"net"
	"strconv"
	"strings"

	"github.com/google/gopacket/routing"
)

const netdir = "/net"

func New() (routing.Router, error) {
	rtr := &router{}
	rtr.ifaces = make(map[int]net.Interface)
	rtr.addrs = make(map[int]ipAddrs)
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("could not get interfaces: %v", err)
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

	rtr.v4, rtr.v6, err = parseIPRoutes()
	if err != nil {
		return nil, err
	}
	return rtr, nil
}

func parseIPRoutes() (v4, v6 routeSlice, err error) {
	buf, err := ioutil.ReadFile(netdir + "/iproute")
	if err != nil {
		return nil, nil, err
	}

	for {
		i := bytes.IndexRune(buf, '\n')
		if i <= 0 {
			break
		}
		f := strings.Fields(string(buf[:i]))
		buf = buf[i+1:]

		if len(f) < 8 {
			return nil, nil, fmt.Errorf("iproute entry contains %d fields", len(f))
		}
		flags, rt, err := parseRoute(f)
		if err != nil {
			return nil, nil, err
		}
		if rt.Dst != nil {
			// If gateway for destination 127.0.0.1/32 is 127.0.0.1, set it to nil.
			if m, n := rt.Dst.Mask.Size(); n > 0 && m == n && rt.Dst.IP.Equal(rt.Gateway) {
				rt.Gateway = nil
			}
		}
		if strings.ContainsRune(flags, '4') { // IPv4
			v4 = append(v4, rt)
		}
		if strings.ContainsRune(flags, '6') { // IPv6
			v6 = append(v6, rt)
		}
	}
	return v4, v6, nil
}

func parseRoute(f []string) (flags string, rt *rtInfo, err error) {
	rt = new(rtInfo)
	isV4 := strings.ContainsRune(f[3], '4') // flags

	rt.PrefSrc, rt.Src, err = parsePlan9CIDR(f[6], f[7], isV4)
	if err != nil {
		return "", nil, err
	}
	_, rt.Dst, err = parsePlan9CIDR(f[0], f[1], isV4)
	if err != nil {
		return "", nil, err
	}
	rt.Gateway = net.ParseIP(f[2])

	n, err := strconv.ParseUint(f[5], 10, 32)
	if err != nil {
		return "", nil, err
	}
	rt.InputIface = 0
	rt.OutputIface = uint32(n) + 1 // starts at 0, so net package adds 1
	rt.Priority = 0
	return f[3], rt, nil
}

func parsePlan9CIDR(addr, mask string, isV4 bool) (net.IP, *net.IPNet, error) {
	if len(mask) == 0 || mask[0] != '/' {
		return nil, nil, fmt.Errorf("invalid CIDR mask %v", mask)
	}
	n, err := strconv.ParseUint(mask[1:], 10, 32)
	if err != nil {
		return nil, nil, err
	}
	ip := net.ParseIP(addr)
	iplen := net.IPv6len
	if isV4 {
		// Plan 9 uses IPv6 mask for IPv4 addresses
		n -= 8 * (net.IPv6len - net.IPv4len)
		iplen = net.IPv4len
	}
	if n == 0 && ip.IsUnspecified() {
		return nil, nil, nil
	}
	m := net.CIDRMask(int(n), 8*iplen)
	return ip, &net.IPNet{IP: ip.Mask(m), Mask: m}, nil
}
