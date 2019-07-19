package network

import (
	"io"
	"os"

	"github.com/moby/buildkit/solver/pb"
	specs "github.com/opencontainers/runtime-spec/specs-go"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

type Opt struct {
	Root          string
	Mode          string
	CNIConfigPath string
	CNIBinaryDir  string
}

// Providers returns the network provider set
func Providers(opt Opt) (map[pb.NetMode]Provider, error) {
	var defaultProvider Provider
	switch opt.Mode {
	case "cni":
		cniProvider, err := NewCNIProvider(opt)
		if err != nil {
			return nil, err
		}
		defaultProvider = cniProvider
	case "host":
		defaultProvider = NewHostProvider()
	case "auto", "":
		if _, err := os.Stat(opt.CNIConfigPath); err == nil {
			cniProvider, err := NewCNIProvider(opt)
			if err != nil {
				return nil, err
			}
			defaultProvider = cniProvider
		} else {
			logrus.Warnf("using host network as the default")
			defaultProvider = NewHostProvider()
		}
	default:
		return nil, errors.Errorf("invalid network mode: %q", opt.Mode)
	}

	return map[pb.NetMode]Provider{
		pb.NetMode_UNSET: defaultProvider,
		pb.NetMode_HOST:  NewHostProvider(),
		pb.NetMode_NONE:  NewNoneProvider(),
	}, nil
}

// Provider interface for Network
type Provider interface {
	New() (Namespace, error)
}

// Namespace of network for workers
type Namespace interface {
	io.Closer
	// Set the namespace on the spec
	Set(*specs.Spec)
}

// NetworkOpts hold network options
type NetworkOpts struct {
	Type          string
	CNIConfigPath string
	CNIPluginPath string
}
