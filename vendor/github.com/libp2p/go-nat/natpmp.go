package nat

import (
	"context"
	"net"
	"time"

	"github.com/jackpal/go-nat-pmp"
)

var (
	_ NAT = (*natpmpNAT)(nil)
)

func discoverNATPMP(ctx context.Context) <-chan NAT {
	res := make(chan NAT, 1)

	ip, err := getDefaultGateway()
	if err != nil {
		return nil
	}

	go func() {
		defer close(res)
		// Unfortunately, we can't actually _stop_ the natpmp
		// library. However, we can at least close _our_ channel
		// and walk away.
		select {
		case client, ok := <-discoverNATPMPWithAddr(ip):
			if ok {
				res <- &natpmpNAT{client, ip, make(map[int]int)}
			}
		case <-ctx.Done():
		}
	}()
	return res
}

func discoverNATPMPWithAddr(ip net.IP) <-chan *natpmp.Client {
	res := make(chan *natpmp.Client, 1)
	go func() {
		defer close(res)
		client := natpmp.NewClient(ip)
		_, err := client.GetExternalAddress()
		if err != nil {
			return
		}
		res <- client
	}()
	return res
}

type natpmpNAT struct {
	c       *natpmp.Client
	gateway net.IP
	ports   map[int]int
}

func (n *natpmpNAT) GetDeviceAddress() (addr net.IP, err error) {
	return n.gateway, nil
}

func (n *natpmpNAT) GetInternalAddress() (addr net.IP, err error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}

	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			return nil, err
		}

		for _, addr := range addrs {
			switch x := addr.(type) {
			case *net.IPNet:
				if x.Contains(n.gateway) {
					return x.IP, nil
				}
			}
		}
	}

	return nil, ErrNoInternalAddress
}

func (n *natpmpNAT) GetExternalAddress() (addr net.IP, err error) {
	res, err := n.c.GetExternalAddress()
	if err != nil {
		return nil, err
	}

	d := res.ExternalIPAddress
	return net.IPv4(d[0], d[1], d[2], d[3]), nil
}

func (n *natpmpNAT) AddPortMapping(protocol string, internalPort int, description string, timeout time.Duration) (int, error) {
	var (
		err error
	)

	timeoutInSeconds := int(timeout / time.Second)

	if externalPort := n.ports[internalPort]; externalPort > 0 {
		_, err = n.c.AddPortMapping(protocol, internalPort, externalPort, timeoutInSeconds)
		if err == nil {
			n.ports[internalPort] = externalPort
			return externalPort, nil
		}
	}

	for i := 0; i < 3; i++ {
		externalPort := randomPort()
		_, err = n.c.AddPortMapping(protocol, internalPort, externalPort, timeoutInSeconds)
		if err == nil {
			n.ports[internalPort] = externalPort
			return externalPort, nil
		}
	}

	return 0, err
}

func (n *natpmpNAT) DeletePortMapping(protocol string, internalPort int) (err error) {
	delete(n.ports, internalPort)
	return nil
}

func (n *natpmpNAT) Type() string {
	return "NAT-PMP"
}
