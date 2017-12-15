package network

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAllocateIP(t *testing.T) {
	t.Parallel()
	ipMgr, _ := InitIPManager("")
	ip := ipMgr.AllocateIP()
	require.Equal(t, "172.17.0.2", ip)

	ipMgr.Release(ip)
	ip1 := ipMgr.AllocateIP()
	ip2 := ipMgr.AllocateIP()
	require.Equal(t, "172.17.0.2", ip1)
	require.Equal(t, "172.17.0.3", ip2)

	ipMgr.Release(ip1)
	ip1 = ipMgr.AllocateIP()
	require.Equal(t, "172.17.0.2", ip1)
	ipMgr.Release(ip1)
	ipMgr.Release(ip2)
}

func TestAllocateIPCustomIP(t *testing.T) {
	t.Parallel()
	ipMgr, _ := InitIPManager("docker0")
	if ipMgr != nil {
		ip := ipMgr.AllocateIP()
		require.Equal(t, "172.17.0.2", ip)

		ipMgr.Release(ip)
		ip1 := ipMgr.AllocateIP()
		ip2 := ipMgr.AllocateIP()
		require.Equal(t, "172.17.0.2", ip1)
		require.Equal(t, "172.17.0.3", ip2)

		ipMgr.Release(ip1)
		ip1 = ipMgr.AllocateIP()
		require.Equal(t, "172.17.0.2", ip1)
		ipMgr.Release(ip1)
		ipMgr.Release(ip2)
	}
}
