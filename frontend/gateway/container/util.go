package container

import (
	"fmt"
	"net"

	"github.com/moby/buildkit/executor"
	"github.com/moby/buildkit/solver/pb"
)

func ParseExtraHosts(ips []*pb.HostIP) ([]executor.HostIP, error) {
	out := make([]executor.HostIP, len(ips))
	for i, hip := range ips {
		ip := net.ParseIP(hip.IP)
		if ip == nil {
			return nil, fmt.Errorf("failed to parse IP %s", hip.IP)
		}
		out[i] = executor.HostIP{
			IP:   ip,
			Host: hip.Host,
		}
	}
	return out, nil
}
