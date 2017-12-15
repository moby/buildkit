package network

import (
	"fmt"
	"net"

	"github.com/moby/buildkit/identity"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
)

type NetworkProvider interface {
	NewInterface() (NetworkInterface, error)
	Release(NetworkInterface) error
}

//NetworkInterface for workers
type NetworkInterface interface {
	// Set the pid with network interace namespace
	Set(int) error
	// Removes the network interface
	Remove(int) error
	// Returns IP is associated with it.
	GetIP() string
}

//BridgeProvider implementes NetworkProvider
type BridgeProvider struct {
	BridgeName string
	ipMgr      IPManager
}

func InitBridgeProvider(name string) (BridgeProvider, error) {
	ipMgr, err := InitIPManager(name)

	return BridgeProvider{
		BridgeName: name,
		ipMgr:      ipMgr,
	}, err
}

//NewInterface creates a veth to bridge the provided ethernet
func (b BridgeProvider) NewInterface() (p NetworkInterface, err error) {
	var (
		peerName string
		linkName string
	)
	if b.ipMgr == nil {
		return nil, fmt.Errorf("ip manager not intialized")
	}

	peerName = getRandomName()
	linkName = getRandomName()
	veth := &netlink.Veth{
		LinkAttrs: netlink.LinkAttrs{
			Name: linkName,
		},
		PeerName: peerName,
	}
	if err := netlink.LinkAdd(veth); err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			netlink.LinkDel(veth)
		}
	}()
	brl, err := netlink.LinkByName(b.BridgeName)
	if err != nil {
		return nil, err
	}
	br, ok := brl.(*netlink.Bridge)
	if !ok {
		return nil, fmt.Errorf("wrong device type %T", brl)
	}
	host, err := netlink.LinkByName(veth.Name)
	if err != nil {
		return nil, err
	}
	if err := netlink.LinkSetMaster(host, br); err != nil {
		return nil, err
	}
	if err := netlink.LinkSetMTU(host, 1500); err != nil {
		return nil, err
	}
	if err := netlink.LinkSetUp(host); err != nil {
		return nil, err
	}

	ip := b.ipMgr.AllocateIP()

	return &VethPair{
		ip:       ip,
		bridgeIP: b.ipMgr.GetBridgeIP(),
		peer:     peerName,
	}, nil
}

func (b BridgeProvider) Release(p NetworkInterface) error {
	ip := p.GetIP()
	if ip != "" {
		b.ipMgr.Release(ip)
	}
	return nil
}

//VethPair spaceholder for functions
type VethPair struct {
	ip       string
	bridgeIP string
	peer     string
}

//Set network namespace of task
func (v *VethPair) Set(pid int) error {
	child, err := netlink.LinkByName(v.peer)
	if err != nil {
		return err
	}

	if err = netlink.LinkSetNsPid(child, pid); err != nil {
		return err
	}

	nsHandle, err := netns.GetFromPid(pid)
	if err != nil {
		return err
	}
	defer nsHandle.Close()

	nlHandle, err := netlink.NewHandleAt(nsHandle)
	if err != nil {
		return err
	}
	defer nlHandle.Delete()

	ip := net.ParseIP(v.ip)
	src := &net.IPNet{IP: ip,
		Mask: net.CIDRMask(24, 32),
	}

	ipAddr := netlink.Addr{IPNet: src}

	if err := nlHandle.LinkSetName(child, "eth0"); err != nil {
		return err
	}

	if err := nlHandle.AddrAdd(child, &ipAddr); err != nil {
		return err
	}
	if err := nlHandle.LinkSetUp(child); err != nil {
		return err
	}

	route := netlink.Route{LinkIndex: child.Attrs().Index, Dst: nil, Gw: net.ParseIP(v.bridgeIP)}

	return nlHandle.RouteAdd(&route)
}

//Remove the link from system
func (v *VethPair) Remove(pid int) error {

	nsHandle, err := netns.GetFromPid(pid)
	if err != nil {
		return err
	}
	defer nsHandle.Close()

	nlHandle, err := netlink.NewHandleAt(nsHandle)
	if err != nil {
		return err
	}
	defer nlHandle.Delete()

	child, err := nlHandle.LinkByName(v.peer)
	if err != nil {
		return err
	}

	return nlHandle.LinkDel(child)
}

func (v *VethPair) GetIP() string {
	return v.ip
}

func getRandomName() string {
	//max length of Network Interface name.
	return identity.NewID()[:15]
}
