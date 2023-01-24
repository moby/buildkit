package nat

import (
	"context"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/huin/goupnp"
	"github.com/huin/goupnp/dcps/internetgateway1"
	"github.com/huin/goupnp/dcps/internetgateway2"

	"github.com/koron/go-ssdp"
)

var (
	_ NAT = (*upnp_NAT)(nil)
)

func discoverUPNP_IG1(ctx context.Context) <-chan NAT {
	res := make(chan NAT)
	go func() {
		defer close(res)

		// find devices
		devs, err := goupnp.DiscoverDevices(internetgateway1.URN_WANConnectionDevice_1)
		if err != nil {
			return
		}

		for _, dev := range devs {
			if dev.Root == nil {
				continue
			}

			dev.Root.Device.VisitServices(func(srv *goupnp.Service) {
				if ctx.Err() != nil {
					return
				}
				switch srv.ServiceType {
				case internetgateway1.URN_WANIPConnection_1:
					client := &internetgateway1.WANIPConnection1{ServiceClient: goupnp.ServiceClient{
						SOAPClient: srv.NewSOAPClient(),
						RootDevice: dev.Root,
						Service:    srv,
					}}
					_, isNat, err := client.GetNATRSIPStatus()
					if err == nil && isNat {
						select {
						case res <- &upnp_NAT{client, make(map[int]int), "UPNP (IG1-IP1)", dev.Root}:
						case <-ctx.Done():
						}
					}

				case internetgateway1.URN_WANPPPConnection_1:
					client := &internetgateway1.WANPPPConnection1{ServiceClient: goupnp.ServiceClient{
						SOAPClient: srv.NewSOAPClient(),
						RootDevice: dev.Root,
						Service:    srv,
					}}
					_, isNat, err := client.GetNATRSIPStatus()
					if err == nil && isNat {
						select {
						case res <- &upnp_NAT{client, make(map[int]int), "UPNP (IG1-PPP1)", dev.Root}:
						case <-ctx.Done():
						}
					}

				}
			})
		}

	}()
	return res
}

func discoverUPNP_IG2(ctx context.Context) <-chan NAT {
	res := make(chan NAT)
	go func() {
		defer close(res)

		// find devices
		devs, err := goupnp.DiscoverDevices(internetgateway2.URN_WANConnectionDevice_2)
		if err != nil {
			return
		}

		for _, dev := range devs {
			if dev.Root == nil {
				continue
			}

			dev.Root.Device.VisitServices(func(srv *goupnp.Service) {
				if ctx.Err() != nil {
					return
				}
				switch srv.ServiceType {
				case internetgateway2.URN_WANIPConnection_1:
					client := &internetgateway2.WANIPConnection1{ServiceClient: goupnp.ServiceClient{
						SOAPClient: srv.NewSOAPClient(),
						RootDevice: dev.Root,
						Service:    srv,
					}}
					_, isNat, err := client.GetNATRSIPStatus()
					if err == nil && isNat {
						select {
						case res <- &upnp_NAT{client, make(map[int]int), "UPNP (IG2-IP1)", dev.Root}:
						case <-ctx.Done():
						}
					}

				case internetgateway2.URN_WANIPConnection_2:
					client := &internetgateway2.WANIPConnection2{ServiceClient: goupnp.ServiceClient{
						SOAPClient: srv.NewSOAPClient(),
						RootDevice: dev.Root,
						Service:    srv,
					}}
					_, isNat, err := client.GetNATRSIPStatus()
					if err == nil && isNat {
						select {
						case res <- &upnp_NAT{client, make(map[int]int), "UPNP (IG2-IP2)", dev.Root}:
						case <-ctx.Done():
						}
					}

				case internetgateway2.URN_WANPPPConnection_1:
					client := &internetgateway2.WANPPPConnection1{ServiceClient: goupnp.ServiceClient{
						SOAPClient: srv.NewSOAPClient(),
						RootDevice: dev.Root,
						Service:    srv,
					}}
					_, isNat, err := client.GetNATRSIPStatus()
					if err == nil && isNat {
						select {
						case res <- &upnp_NAT{client, make(map[int]int), "UPNP (IG2-PPP1)", dev.Root}:
						case <-ctx.Done():
						}
					}

				}
			})
		}

	}()
	return res
}

func discoverUPNP_GenIGDev(ctx context.Context) <-chan NAT {
	res := make(chan NAT, 1)
	go func() {
		defer close(res)

		DeviceList, err := ssdp.Search(ssdp.All, 5, "")
		if err != nil {
			return
		}
		var gw ssdp.Service
		for _, Service := range DeviceList {
			if strings.Contains(Service.Type, "InternetGatewayDevice") {
				gw = Service
				break
			}
		}

		DeviceURL, err := url.Parse(gw.Location)
		if err != nil {
			return
		}
		RootDevice, err := goupnp.DeviceByURL(DeviceURL)
		if err != nil {
			return
		}

		RootDevice.Device.VisitServices(func(srv *goupnp.Service) {
			if ctx.Err() != nil {
				return
			}
			switch srv.ServiceType {
			case internetgateway1.URN_WANIPConnection_1:
				client := &internetgateway1.WANIPConnection1{ServiceClient: goupnp.ServiceClient{
					SOAPClient: srv.NewSOAPClient(),
					RootDevice: RootDevice,
					Service:    srv,
				}}
				_, isNat, err := client.GetNATRSIPStatus()
				if err == nil && isNat {
					select {
					case res <- &upnp_NAT{client, make(map[int]int), "UPNP (IG1-IP1)", RootDevice}:
					case <-ctx.Done():
					}
				}

			case internetgateway1.URN_WANPPPConnection_1:
				client := &internetgateway1.WANPPPConnection1{ServiceClient: goupnp.ServiceClient{
					SOAPClient: srv.NewSOAPClient(),
					RootDevice: RootDevice,
					Service:    srv,
				}}
				_, isNat, err := client.GetNATRSIPStatus()
				if err == nil && isNat {
					select {
					case res <- &upnp_NAT{client, make(map[int]int), "UPNP (IG1-PPP1)", RootDevice}:
					case <-ctx.Done():
					}
				}

			}
		})
	}()
	return res
}

type upnp_NAT_Client interface {
	GetExternalIPAddress() (string, error)
	AddPortMapping(string, uint16, string, uint16, string, bool, string, uint32) error
	DeletePortMapping(string, uint16, string) error
}

type upnp_NAT struct {
	c          upnp_NAT_Client
	ports      map[int]int
	typ        string
	rootDevice *goupnp.RootDevice
}

func (u *upnp_NAT) GetExternalAddress() (addr net.IP, err error) {
	ipString, err := u.c.GetExternalIPAddress()
	if err != nil {
		return nil, err
	}

	ip := net.ParseIP(ipString)
	if ip == nil {
		return nil, ErrNoExternalAddress
	}

	return ip, nil
}

func mapProtocol(s string) string {
	switch s {
	case "udp":
		return "UDP"
	case "tcp":
		return "TCP"
	default:
		panic("invalid protocol: " + s)
	}
}

func (u *upnp_NAT) AddPortMapping(protocol string, internalPort int, description string, timeout time.Duration) (int, error) {
	ip, err := u.GetInternalAddress()
	if err != nil {
		return 0, nil
	}

	timeoutInSeconds := uint32(timeout / time.Second)

	if externalPort := u.ports[internalPort]; externalPort > 0 {
		err = u.c.AddPortMapping("", uint16(externalPort), mapProtocol(protocol), uint16(internalPort), ip.String(), true, description, timeoutInSeconds)
		if err == nil {
			return externalPort, nil
		}
	}

	for i := 0; i < 3; i++ {
		externalPort := randomPort()
		err = u.c.AddPortMapping("", uint16(externalPort), mapProtocol(protocol), uint16(internalPort), ip.String(), true, description, timeoutInSeconds)
		if err == nil {
			u.ports[internalPort] = externalPort
			return externalPort, nil
		}
	}

	return 0, err
}

func (u *upnp_NAT) DeletePortMapping(protocol string, internalPort int) error {
	if externalPort := u.ports[internalPort]; externalPort > 0 {
		delete(u.ports, internalPort)
		return u.c.DeletePortMapping("", uint16(externalPort), mapProtocol(protocol))
	}

	return nil
}

func (u *upnp_NAT) GetDeviceAddress() (net.IP, error) {
	addr, err := net.ResolveUDPAddr("udp4", u.rootDevice.URLBase.Host)
	if err != nil {
		return nil, err
	}

	return addr.IP, nil
}

func (u *upnp_NAT) GetInternalAddress() (net.IP, error) {
	devAddr, err := u.GetDeviceAddress()
	if err != nil {
		return nil, err
	}

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
				if x.Contains(devAddr) {
					return x.IP, nil
				}
			}
		}
	}

	return nil, ErrNoInternalAddress
}

func (n *upnp_NAT) Type() string { return n.typ }
