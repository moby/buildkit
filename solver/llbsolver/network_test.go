package llbsolver

import "github.com/moby/buildkit/util/network"

var _ network.ProxyPolicy = (*policyEvaluator)(nil)
