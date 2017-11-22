package bridge

import (
	"fmt"
	"math/rand"
	"strconv"
	"time"

	"github.com/vishvananda/netlink"
)

//CreateBridgePair creats a veth to bridge the provided ethernet
func CreateBridgePair(bridge string) (p *VethPair, err error) {
	var (
		peerName string
		linkName string
	)
	peerName = getRandomName("vethBkit")
	linkName = getRandomName("bkitWkr")
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
	brl, err := netlink.LinkByName(bridge)
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
	return &VethPair{
		peer: peerName,
	}, nil
}

//VethPair spaceholder for functions
type VethPair struct {
	peer string
}

//Set network namespace of task
func (v *VethPair) Set(pid int) error {
	child, err := netlink.LinkByName(v.peer)
	if err != nil {
		return err
	}
	return netlink.LinkSetNsPid(child, pid)
}

//Remove the link from system
func (v *VethPair) Remove() error {
	child, err := netlink.LinkByName(v.peer)
	if err != nil {
		return err
	}
	return netlink.LinkDel(child)
}

func getRandomName(prefix string) string {
	source := rand.NewSource(time.Now().UnixNano())
	random := rand.New(source)
	suffixNum := random.Intn(999)
	return prefix + strconv.Itoa(suffixNum)
}
