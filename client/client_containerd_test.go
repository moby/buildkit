// +build containerd

package client

import (
	"testing"
	"time"
)

func setupContainerd() (func(), error) {
	containerdSock, cleanupContainerd, err := runContainerd()
	if err != nil {
		return nil, err
	}
	sock, cleanup, err := runBuildd([]string{"buildd-containerd", "--containerd", containerdSock})
	if err != nil {
		cleanupContainerd()
		return nil, err
	}

	clientAddressContainerd = sock
	time.Sleep(100 * time.Millisecond) // TODO
	return func() {
		cleanup()
		cleanupContainerd()
	}, nil
}

func TestCallDiskUsageContainerd(t *testing.T) {
	testCallDiskUsage(t, clientAddressContainerd)
}

func TestBuildMultiMountContainerd(t *testing.T) {
	testBuildMultiMount(t, clientAddressContainerd)
}
