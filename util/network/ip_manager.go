package network

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
)

//IPPrefixDefault used for IP prefix for AllocateIP
const IPPrefixDefault = "172.17.0."

//DefaultBridgeIP used for setting gateway
const DefaultBridgeIP = "172.17.0.1"

//IPManager interface for IPManagement functions
type IPManager interface {
	AllocateIP() string
	Release(string)
	GetBridgeIP() string
}

//BasicIPManager implements IPManager
type BasicIPManager struct {
	sync.RWMutex
	IPTable  map[int]bool
	IPPrefix string
	BridgeIP string
}

//InitIPManager creates IPManager object
func InitIPManager(bridgeName string) (IPManager, error) {
	ipMgr := new(BasicIPManager)
	ipMgr.IPTable = make(map[int]bool)

	if bridgeName == "" {
		ipMgr.IPPrefix = IPPrefixDefault
		ipMgr.BridgeIP = DefaultBridgeIP
	} else {
		ip := getIPv4ForInterfaceName(bridgeName)
		if ip == nil {
			return nil, fmt.Errorf("Invalid bridge :%s", bridgeName)
		}
		bridgeIP := ip.String()
		ipTokens := strings.SplitAfterN(bridgeIP, ".", 4)
		ipPrefix := ipTokens[0] + ipTokens[1] + ipTokens[2]
		ipMgr.IPPrefix = ipPrefix
		ipMgr.BridgeIP = bridgeIP
	}
	//248 Workers at one time. Is that fine?
	for i := 0; i < 250; i++ {
		ipMgr.IPTable[i] = false
	}
	return ipMgr, nil
}

//AllocateIP allocates the IP from avaible pool
func (ipMgr *BasicIPManager) AllocateIP() string {
	// 0 and 1 are reserved.
	for i := 2; i < 250; i++ {
		ipMgr.RLock()
		if ipMgr.IPTable[i] == false {
			ipMgr.RUnlock()
			ipMgr.Lock()
			ipMgr.IPTable[i] = true
			ipMgr.Unlock()
			return ipMgr.IPPrefix + strconv.Itoa(i)
		}
		ipMgr.RUnlock()
	}
	return ""
}

//Release marks the IP to free
func (ipMgr *BasicIPManager) Release(ip string) {
	ipTolken := strings.SplitN(ip, ".", 4)
	ipInt, _ := strconv.Atoi(ipTolken[3])
	ipMgr.Lock()
	ipMgr.IPTable[ipInt] = false
	ipMgr.Unlock()
}

//GetBridgeIP return bridge IP
func (ipMgr *BasicIPManager) GetBridgeIP() string {
	return ipMgr.BridgeIP
}

func getIPv4ForInterfaceName(ifname string) (ifaceip net.IP) {
	interfaces, _ := net.Interfaces()
	for _, inter := range interfaces {
		if inter.Name == ifname {
			if addrs, err := inter.Addrs(); err == nil {
				for _, addr := range addrs {
					switch ip := addr.(type) {
					case *net.IPNet:
						if ip.IP.DefaultMask() != nil {
							return (ip.IP)
						}
					}
				}
			}
		}
	}
	return nil
}
