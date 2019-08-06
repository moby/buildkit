package netproviders

import (
	"os"

	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/network"
	"github.com/moby/buildkit/util/network/cniprovider"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

type Opt struct {
	CNI  cniprovider.Opt
	Mode string
}

// Providers returns the network provider set
func Providers(opt Opt) (map[pb.NetMode]network.Provider, error) {
	var defaultProvider network.Provider
	switch opt.Mode {
	case "cni":
		cniProvider, err := cniprovider.New(opt.CNI)
		if err != nil {
			return nil, err
		}
		defaultProvider = cniProvider
	case "host":
		defaultProvider = network.NewHostProvider()
	case "auto", "":
		if _, err := os.Stat(opt.CNI.ConfigPath); err == nil {
			cniProvider, err := cniprovider.New(opt.CNI)
			if err != nil {
				return nil, err
			}
			defaultProvider = cniProvider
		} else {
			logrus.Warnf("using host network as the default")
			defaultProvider = network.NewHostProvider()
		}
	default:
		return nil, errors.Errorf("invalid network mode: %q", opt.Mode)
	}

	return map[pb.NetMode]network.Provider{
		pb.NetMode_UNSET: defaultProvider,
		pb.NetMode_HOST:  network.NewHostProvider(),
		pb.NetMode_NONE:  network.NewNoneProvider(),
	}, nil
}
