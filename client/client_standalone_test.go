// +build standalone

package client

import (
	"testing"
	"time"
)

func setupStandalone() (func(), error) {
	sock, close, err := runBuildd([]string{"buildd-standalone"})
	if err != nil {
		return nil, err
	}
	clientAddressStandalone = sock
	time.Sleep(100 * time.Millisecond) // TODO
	return close, nil
}

func TestCallDiskUsageStandalone(t *testing.T) {
	testCallDiskUsage(t, clientAddressStandalone)
}

func TestBuildMultiMountStandalone(t *testing.T) {
	testBuildMultiMount(t, clientAddressStandalone)
}
