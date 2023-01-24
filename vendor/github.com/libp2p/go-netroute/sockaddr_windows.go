//go:build windows
// +build windows

package netroute

import (
	"net"
	"strconv"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// socklen is a type for the length of a sockaddr.
type socklen uint

// ipAndZoneToSockaddr converts a net.IP (with optional IPv6 Zone) to a Sockaddr
// Returns nil if conversion fails.
func ipAndZoneToSockaddr(ip net.IP, zone string) windows.Sockaddr {
	// Unspecified?
	if ip == nil {
		if zone != "" {
			return &windows.SockaddrInet6{ZoneId: uint32(ip6ZoneToInt(zone))}
		}
		return new(windows.SockaddrInet4)
	}

	// Valid IPv4?
	if ip4 := ip.To4(); ip4 != nil && zone == "" {
		var buf [4]byte
		copy(buf[:], ip4) // last 4 bytes
		return &windows.SockaddrInet4{Addr: buf}
	}

	// Valid IPv6 address?
	if ip6 := ip.To16(); ip6 != nil {
		var buf [16]byte
		copy(buf[:], ip6)
		return &windows.SockaddrInet6{Addr: buf, ZoneId: uint32(ip6ZoneToInt(zone))}
	}

	return nil
}

// sockaddrToIPAndZone converts a Sockaddr to a net.IP (with optional IPv6 Zone)
// Returns nil if conversion fails.
func sockaddrToIPAndZone(sa windows.Sockaddr) (net.IP, string) {
	switch sa := sa.(type) {
	case *windows.SockaddrInet4:
		ip := make([]byte, 16)
		// V4InV6Prefix
		ip[10] = 0xff
		ip[11] = 0xff
		copy(ip[12:16], sa.Addr[:])
		return ip, ""
	case *windows.SockaddrInet6:
		ip := make([]byte, 16)
		copy(ip, sa.Addr[:])
		return ip, ip6ZoneToString(int(sa.ZoneId))
	}
	return nil, ""
}

func sockaddrToAny(sa windows.Sockaddr) (*windows.RawSockaddrAny, socklen, error) {
	if sa == nil {
		return nil, 0, syscall.EINVAL
	}

	switch sa := sa.(type) {
	case *windows.SockaddrInet4:
		if sa.Port < 0 || sa.Port > 0xFFFF {
			return nil, 0, syscall.EINVAL
		}
		raw := new(windows.RawSockaddrAny)
		raw.Addr.Family = windows.AF_INET
		raw4 := (*windows.RawSockaddrInet4)(unsafe.Pointer(raw))
		p := (*[2]byte)(unsafe.Pointer(&raw4.Port))
		p[0] = byte(sa.Port >> 8)
		p[1] = byte(sa.Port)
		for i := 0; i < len(sa.Addr); i++ {
			raw4.Addr[i] = sa.Addr[i]
		}
		return raw, socklen(unsafe.Sizeof(*raw4)), nil
	case *windows.SockaddrInet6:
		if sa.Port < 0 || sa.Port > 0xFFFF {
			return nil, 0, syscall.EINVAL
		}
		raw := new(windows.RawSockaddrAny)
		raw.Addr.Family = windows.AF_INET6
		raw6 := (*windows.RawSockaddrInet6)(unsafe.Pointer(raw))
		p := (*[2]byte)(unsafe.Pointer(&raw6.Port))
		p[0] = byte(sa.Port >> 8)
		p[1] = byte(sa.Port)
		raw6.Scope_id = sa.ZoneId
		for i := 0; i < len(sa.Addr); i++ {
			raw6.Addr[i] = sa.Addr[i]
		}
		return raw, socklen(unsafe.Sizeof(*raw6)), nil
	case *windows.SockaddrUnix:
		return nil, 0, syscall.EWINDOWS
	}
	return nil, 0, syscall.EAFNOSUPPORT
}

// from: go/src/pkg/net/ipsock.go

// ip6ZoneToString converts an IP6 Zone unix int to a net string
// returns "" if zone is 0
func ip6ZoneToString(zone int) string {
	if zone == 0 {
		return ""
	}
	if ifi, err := net.InterfaceByIndex(zone); err == nil {
		return ifi.Name
	}
	return strconv.Itoa(zone)
}

// ip6ZoneToInt converts an IP6 Zone net string to a unix int
// returns 0 if zone is ""
func ip6ZoneToInt(zone string) int {
	if zone == "" {
		return 0
	}
	if ifi, err := net.InterfaceByName(zone); err == nil {
		return ifi.Index
	}
	n, _ := strconv.ParseInt(zone, 10, 32)
	return int(n)
}
