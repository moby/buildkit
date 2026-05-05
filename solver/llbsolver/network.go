package llbsolver

import (
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/sourcepolicy"
	spb "github.com/moby/buildkit/sourcepolicy/pb"
	"github.com/moby/buildkit/util/network"
	"github.com/pkg/errors"
)

func setProxyNetwork(op *pb.Op, proxyNetwork bool) error {
	exec := op.GetExec()
	if exec == nil {
		return nil
	}
	if !proxyNetwork {
		if exec.Network == pb.NetMode_PROXY {
			return errors.Errorf("network mode %s requires proxy network to be enabled for the build", exec.Network)
		}
		return nil
	}
	switch exec.Network {
	case pb.NetMode_UNSET:
		exec.Network = pb.NetMode_PROXY
	case pb.NetMode_NONE, pb.NetMode_PROXY:
		return nil
	default:
		return errors.Errorf("network mode %s is not allowed when proxy network is enabled", exec.Network)
	}
	return nil
}

func (b *provenanceBridge) ProxyPolicy() (network.ProxyPolicy, error) {
	return b.llbBridge.ProxyPolicy()
}

func (b *llbBridge) ProxyPolicy() (network.ProxyPolicy, error) {
	srcPol, err := loadSourcePolicy(b.builder)
	if err != nil {
		return nil, err
	}
	policySession, err := loadSourcePolicySession(b.builder)
	if err != nil {
		return nil, err
	}
	if (srcPol == nil || len(srcPol.Rules) == 0) && policySession == "" {
		return nil, nil
	}
	var policies []*spb.Policy
	if srcPol != nil {
		policies = append(policies, srcPol)
	}
	return b.policy(sourcepolicy.NewEngine(policies)), nil
}
