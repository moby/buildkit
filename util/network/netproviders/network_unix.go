//go:build !windows
// +build !windows

package netproviders

import (
	"github.com/moby/buildkit/util/network"
	"github.com/sirupsen/logrus"
)

func getHostProvider() (network.Provider, bool) {
	return network.NewHostProvider(), true
}

func getFallback() network.Provider {
	logrus.Warn("using host network as the default")
	return network.NewHostProvider()
}
