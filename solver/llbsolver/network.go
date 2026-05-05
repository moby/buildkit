package llbsolver

import (
	"context"

	gatewaypb "github.com/moby/buildkit/frontend/gateway/pb"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/sourcepolicy"
	spb "github.com/moby/buildkit/sourcepolicy/pb"
	"github.com/moby/buildkit/sourcepolicy/policysession"
	"github.com/moby/buildkit/util/network"
	"github.com/moby/buildkit/util/urlutil"
	"github.com/pkg/errors"
)

func WithProxyNetwork(proxyNetwork bool) LoadOpt {
	return func(op *pb.Op, _ *pb.OpMetadata, _ *solver.VertexOptions) error {
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
}

func newProxyPolicy(sm *session.Manager, srcPol *spb.Policy, policySession string) network.ProxyPolicy {
	if srcPol == nil && policySession == "" {
		return nil
	}
	var engine *sourcepolicy.Engine
	if srcPol != nil {
		engine = sourcepolicy.NewEngine([]*spb.Policy{srcPol})
	}
	return &proxyPolicy{
		engine:        engine,
		sm:            sm,
		policySession: policySession,
	}
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
	return newProxyPolicy(b.sm, srcPol, policySession), nil
}

type proxyPolicy struct {
	engine        *sourcepolicy.Engine
	sm            *session.Manager
	policySession string
}

func (p *proxyPolicy) CheckProxyRequest(ctx context.Context, url string) error {
	redactedURL := urlutil.RedactCredentials(url)
	op := &pb.Op{
		Op: &pb.Op_Source{
			Source: &pb.SourceOp{
				Identifier: redactedURL,
			},
		},
	}
	if p.engine != nil {
		if _, err := p.engine.Evaluate(ctx, op.GetSource()); err != nil {
			return err
		}
	}
	if p.policySession == "" {
		return nil
	}
	if p.sm == nil {
		return errors.Errorf("source policy session %q is set but session manager is unavailable", p.policySession)
	}
	caller, err := p.sm.Get(ctx, p.policySession, false)
	if err != nil {
		return err
	}
	verifier := policysession.NewPolicyVerifierClient(caller.Conn())
	resp, err := verifier.CheckPolicy(ctx, &policysession.CheckPolicyRequest{
		Source: &gatewaypb.ResolveSourceMetaResponse{
			Source: op.GetSource(),
		},
	})
	if err != nil {
		return err
	}
	if resp.GetRequest() != nil {
		return errors.Errorf("source policy metadata requests are not supported for proxy request %q", redactedURL)
	}
	decision := resp.GetDecision()
	if decision == nil {
		return errors.Errorf("no decision in policy response")
	}
	if decision.Action == spb.PolicyAction_DENY {
		return errors.Wrapf(sourcepolicy.ErrSourceDenied, "source %q denied by policy", redactedURL)
	}
	if decision.Action == spb.PolicyAction_CONVERT {
		return errors.Errorf("source policy convert action is not supported for proxy request %q", redactedURL)
	}
	return nil
}
