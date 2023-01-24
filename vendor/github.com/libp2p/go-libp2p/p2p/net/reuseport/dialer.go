package reuseport

import (
	"context"
	"fmt"
	"math/rand"
	"net"

	"github.com/libp2p/go-netroute"
)

type dialer struct {
	// All address that are _not_ loopback or unspecified (0.0.0.0 or ::).
	specific []*net.TCPAddr
	// All loopback addresses (127.*.*.*, ::1).
	loopback []*net.TCPAddr
	// Unspecified addresses (0.0.0.0, ::)
	unspecified []*net.TCPAddr
}

func (d *dialer) Dial(network, addr string) (net.Conn, error) {
	return d.DialContext(context.Background(), network, addr)
}

func randAddr(addrs []*net.TCPAddr) *net.TCPAddr {
	if len(addrs) > 0 {
		return addrs[rand.Intn(len(addrs))]
	}
	return nil
}

// DialContext dials a target addr.
//
// In-order:
//
//  1. If we're _explicitly_ listening on the prefered source address for the destination address
//     (per the system's routes), we'll use that listener's port as the source port.
//  2. If we're listening on one or more _unspecified_ addresses (zero address), we'll pick a source
//     port from one of these listener's.
//  3. Otherwise, we'll let the system pick the source port.
func (d *dialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	// We only check this case if the user is listening on a specific address (loopback or
	// otherwise). Generally, users will listen on the "unspecified" address (0.0.0.0 or ::) and
	// we can skip this section.
	//
	// This lets us avoid resolving the address twice, in most cases.
	if len(d.specific) > 0 || len(d.loopback) > 0 {
		tcpAddr, err := net.ResolveTCPAddr(network, addr)
		if err != nil {
			return nil, err
		}
		ip := tcpAddr.IP
		if !ip.IsLoopback() && !ip.IsGlobalUnicast() {
			return nil, fmt.Errorf("undialable IP: %s", ip)
		}

		// If we're listening on some specific address and that specific address happens to
		// be the preferred source address for the target destination address, we try to
		// dial with that address/port.
		//
		// We skip this check if we _aren't_ listening on any specific addresses, because
		// checking routing tables can be expensive and users rarely listen on specific IP
		// addresses.
		if len(d.specific) > 0 {
			if router, err := netroute.New(); err == nil {
				if _, _, preferredSrc, err := router.Route(ip); err == nil {
					for _, optAddr := range d.specific {
						if optAddr.IP.Equal(preferredSrc) {
							return reuseDial(ctx, optAddr, network, addr)
						}
					}
				}
			}
		}

		// Otherwise, if we are listening on a loopback address and the destination is also
		// a loopback address, use the port from our loopback listener.
		if len(d.loopback) > 0 && ip.IsLoopback() {
			return reuseDial(ctx, randAddr(d.loopback), network, addr)
		}
	}

	// If we're listening on any uspecified addresses, use a randomly chosen port from one of
	// these listeners.
	if len(d.unspecified) > 0 {
		return reuseDial(ctx, randAddr(d.unspecified), network, addr)
	}

	// Finally, just pick a random port.
	var dialer net.Dialer
	return dialer.DialContext(ctx, network, addr)
}

func newDialer(listeners map[*listener]struct{}) *dialer {
	specific := make([]*net.TCPAddr, 0)
	loopback := make([]*net.TCPAddr, 0)
	unspecified := make([]*net.TCPAddr, 0)

	for l := range listeners {
		addr := l.Addr().(*net.TCPAddr)
		if addr.IP.IsLoopback() {
			loopback = append(loopback, addr)
		} else if addr.IP.IsUnspecified() {
			unspecified = append(unspecified, addr)
		} else {
			specific = append(specific, addr)
		}
	}
	return &dialer{
		specific:    specific,
		loopback:    loopback,
		unspecified: unspecified,
	}
}
