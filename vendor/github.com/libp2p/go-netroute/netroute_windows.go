//go:build windows
// +build windows

package netroute

// Implementation Warning: mapping of the correct interface ID and index is not
// hooked up.
// Reference:
// https://docs.microsoft.com/en-us/windows/win32/api/netioapi/nf-netioapi-getbestroute2
import (
	"bytes"
	"encoding/binary"
	"fmt"
	"net"
	"unsafe"

	"github.com/google/gopacket/routing"
	"golang.org/x/sys/windows"
)

var (
	modiphlpapi       = windows.NewLazyDLL("iphlpapi.dll")
	procGetBestRoute2 = modiphlpapi.NewProc("GetBestRoute2")
)

type NetLUID uint64

type AddressPrefix struct {
	*windows.RawSockaddrAny
	PrefixLength byte
}

type RouteProtocol uint32 // MIB_IPFORWARD_PROTO

// https://docs.microsoft.com/en-us/windows/win32/api/netioapi/ns-netioapi-mib_ipforward_row2
type mib_row2 struct {
	luid              NetLUID
	index             uint32
	destinationPrefix *AddressPrefix
	nextHop           *windows.RawSockaddrAny
	prefixLength      byte
	lifetime          uint32
	preferredLifetime uint32
	metric            uint32
	protocol          RouteProtocol
	loopback          byte
	autoconfigured    byte
	publish           byte
	immortal          byte
	age               uint32
	origin            byte
}

func callBestRoute(source, dest net.IP) (*mib_row2, net.IP, error) {
	sourceAddr, _, _ := sockaddrToAny(ipAndZoneToSockaddr(source, ""))
	destAddr, _, _ := sockaddrToAny(ipAndZoneToSockaddr(dest, ""))
	bestRoute := make([]byte, 512)
	bestSource := make([]byte, 116)

	err := getBestRoute2(nil, 0, sourceAddr, destAddr, 0, bestRoute, bestSource)
	if err != nil {
		return nil, nil, err
	}

	// interpret best route and best source.
	route, err := parseRoute(bestRoute)
	if err != nil {
		return nil, nil, err
	}

	// per https://docs.microsoft.com/en-us/windows/win32/api/netioapi/ns-netioapi-mib_ipforward_row2
	// If the route is to a local loopback address or an IP address on the local link, the next hop is unspecified (all zeros)
	if isZero(route.nextHop) {
		route.nextHop = nil
	}

	var bestSourceRaw windows.RawSockaddrAny
	bestSourceRaw.Addr.Family = binary.LittleEndian.Uint16(bestSource[0:2])
	copyInto(bestSourceRaw.Addr.Data[:], bestSource[2:16])
	copyInto(bestSourceRaw.Pad[:], bestSource[16:])
	addr, _ := bestSourceRaw.Sockaddr()
	bestSrc, _ := sockaddrToIPAndZone(addr)

	return route, bestSrc, nil
}

func copyInto(dst []int8, src []byte) {
	for i, b := range src {
		dst[i] = int8(b)
	}
}

func isZero(addr *windows.RawSockaddrAny) bool {
	for _, b := range addr.Addr.Data {
		if b != 0 {
			return false
		}
	}
	for _, b := range addr.Pad {
		if b != 0 {
			return false
		}
	}
	return true
}

func parseRoute(mib []byte) (*mib_row2, error) {
	var route mib_row2
	var err error

	route.luid = NetLUID(binary.LittleEndian.Uint64(mib[0:]))
	route.index = binary.LittleEndian.Uint32(mib[8:])
	idx := 0
	route.destinationPrefix, idx, err = readDestPrefix(mib, 12)
	if err != nil {
		return nil, err
	}
	route.nextHop, idx, err = readSockAddr(mib, idx)
	if err != nil {
		return nil, err
	}
	route.prefixLength = mib[idx]
	idx += 1
	route.lifetime = binary.LittleEndian.Uint32(mib[idx : idx+4])
	idx += 4
	route.preferredLifetime = binary.LittleEndian.Uint32(mib[idx : idx+4])
	idx += 4
	route.metric = binary.LittleEndian.Uint32(mib[idx : idx+4])
	idx += 4
	route.protocol = RouteProtocol(binary.LittleEndian.Uint32(mib[idx : idx+4]))
	idx += 4
	route.loopback = mib[idx]
	idx += 1
	route.autoconfigured = mib[idx]
	idx += 1
	route.publish = mib[idx]
	idx += 1
	route.immortal = mib[idx]
	idx += 1
	route.age = binary.LittleEndian.Uint32(mib[idx : idx+4])
	idx += 4
	route.origin = mib[idx]

	return &route, err
}

func readDestPrefix(buffer []byte, idx int) (*AddressPrefix, int, error) {
	sock, idx2, err := readSockAddr(buffer, idx)
	if err != nil {
		return nil, 0, err
	}
	pfixLen := buffer[idx2]
	if idx2-idx > 32 {
		return nil, idx, fmt.Errorf("Unexpectedly large internal sockaddr struct")
	}
	return &AddressPrefix{sock, pfixLen}, idx + 32, nil
}

func readSockAddr(buffer []byte, idx int) (*windows.RawSockaddrAny, int, error) {
	var rsa windows.RawSockaddrAny
	rsa.Addr.Family = binary.LittleEndian.Uint16(buffer[idx : idx+2])
	if rsa.Addr.Family == windows.AF_INET || rsa.Addr.Family == windows.AF_UNSPEC {
		copyInto(rsa.Addr.Data[:], buffer[idx+2:idx+16])
		return &rsa, idx + 16, nil
	} else if rsa.Addr.Family == windows.AF_INET6 {
		copyInto(rsa.Addr.Data[:], buffer[idx+2:idx+16])
		copyInto(rsa.Pad[:], buffer[idx+16:idx+28])
		return &rsa, idx + 28, nil
	} else {
		return nil, 0, fmt.Errorf("Unknown windows addr family %d", rsa.Addr.Family)
	}
}

func getBestRoute2(interfaceLuid *NetLUID, interfaceIndex uint32, sourceAddress, destinationAddress *windows.RawSockaddrAny, addressSortOptions uint32, bestRoute []byte, bestSourceAddress []byte) (errcode error) {
	r0, _, _ := procGetBestRoute2.Call(
		uintptr(unsafe.Pointer(interfaceLuid)),
		uintptr(interfaceIndex),
		uintptr(unsafe.Pointer(sourceAddress)),
		uintptr(unsafe.Pointer(destinationAddress)),
		uintptr(addressSortOptions),
		uintptr(unsafe.Pointer(&bestRoute[0])),
		uintptr(unsafe.Pointer(&bestSourceAddress[0])))
	if r0 != 0 {
		errcode = windows.Errno(r0)
	}
	return
}

func getIface(index uint32) *net.Interface {
	var ifRow windows.MibIfRow
	ifRow.Index = index
	err := windows.GetIfEntry(&ifRow)
	if err != nil {
		return nil
	}

	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	for _, iface := range ifaces {
		if bytes.Equal(iface.HardwareAddr, ifRow.PhysAddr[:]) {
			return &iface
		}
	}
	return nil
}

type winRouter struct{}

func (r *winRouter) Route(dst net.IP) (iface *net.Interface, gateway, preferredSrc net.IP, err error) {
	return r.RouteWithSrc(nil, nil, dst)
}

func (r *winRouter) RouteWithSrc(input net.HardwareAddr, src, dst net.IP) (iface *net.Interface, gateway, preferredSrc net.IP, err error) {
	route, pref, err := callBestRoute(src, dst)
	if err != nil {
		return nil, nil, nil, err
	}
	iface = getIface(route.index)

	if route.nextHop == nil || route.nextHop.Addr.Family == 0 /* AF_UNDEF */ {
		return iface, nil, pref, nil
	}
	addr, err := route.nextHop.Sockaddr()
	if err != nil {
		return nil, nil, nil, err
	}
	nextHop, _ := sockaddrToIPAndZone(addr)

	return iface, nextHop, pref, nil
}

func New() (routing.Router, error) {
	rtr := &winRouter{}
	return rtr, nil
}
