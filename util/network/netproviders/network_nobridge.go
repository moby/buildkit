//go:build !linux

package netproviders

import (
	"fmt"
	"runtime"

	"github.com/moby/buildkit/util/network"
	"github.com/moby/buildkit/util/network/cniprovider"
)

func getBridgeProvider(_ cniprovider.Opt) (network.Provider, error) {
	return nil, fmt.Errorf("bridge network is not supported on %s yet", runtime.GOOS)
}
