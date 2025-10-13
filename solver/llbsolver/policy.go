package llbsolver

import (
	"context"
	"strings"

	"github.com/moby/buildkit/client/llb/sourceresolver"
	gatewaypb "github.com/moby/buildkit/frontend/gateway/pb"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/sourcepolicy"
	spb "github.com/moby/buildkit/sourcepolicy/pb"
	"github.com/moby/buildkit/sourcepolicy/policysession"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

type policyEvaluator struct {
	*llbBridge
	engine *sourcepolicy.Engine
}

func (p *policyEvaluator) Evaluate(ctx context.Context, op *pb.Op) (bool, error) {
	source := op.GetSource()
	if source == nil {
		return false, nil
	}
	ok, err := p.engine.Evaluate(ctx, source)
	if err != nil {
		return false, err
	}
	sid, err := loadSourcePolicySession(p.builder)
	if err != nil {
		return false, err
	}
	if sid == "" {
		return ok, nil
	}
	caller, err := p.sm.Get(ctx, sid, false)
	if err != nil {
		return false, err
	}

	verifier := policysession.NewPolicyVerifierClient(caller.Conn())
	req := &policysession.CheckPolicyRequest{
		Platform: op.Platform,
		Source: &gatewaypb.ResolveSourceMetaResponse{
			Source: source,
		},
	}

	max := 0
	for {
		max++
		if max > 10 { // TODO: better loop detection
			return false, errors.Errorf("too many policy requests")
		}
		resp, err := verifier.CheckPolicy(ctx, req)
		if err != nil {
			return false, err
		}

		metareq := resp.GetRequest()
		if metareq != nil {
			op := sourceresolver.Opt{
				LogName:  metareq.LogName,
				Platform: toOCIPlatform(metareq.Platform),
			}
			if metareq.Source.Identifier != source.Identifier {
				return false, errors.Errorf("policy requested different source identifier: %q != %q", metareq.Source.Identifier, source.Identifier)
			}
			if err := mapsEqual(source.Attrs, metareq.Source.Attrs); err != nil {
				return false, errors.Wrap(err, "policy requested different source attrs")
			}
			if metareq.ResolveMode != "" {
				if strings.HasPrefix(metareq.Source.Identifier, "docker-image://") {
					op.ImageOpt = &sourceresolver.ResolveImageOpt{
						ResolveMode: metareq.ResolveMode,
					}
				}
			}
			resp, err := p.resolveSourceMetadata(ctx, metareq.Source, op, false)
			if err != nil {
				return false, errors.Wrap(err, "error resolving source metadata from policy request")
			}
			req.Source = &gatewaypb.ResolveSourceMetaResponse{
				Source: resp.Op,
			}
			if resp.Image != nil {
				req.Source.Image = &gatewaypb.ResolveSourceImageResponse{
					Digest: resp.Image.Digest.String(),
					Config: resp.Image.Config,
				}
			}
			continue
		}

		decision := resp.GetDecision()
		if decision == nil {
			return false, errors.Errorf("no decision in policy response")
		}
		if decision.Action == spb.PolicyAction_CONVERT {
			return false, errors.Errorf("convert action not yet supported")
		}
		if decision.Action != spb.PolicyAction_ALLOW {
			return false, errors.Errorf("source %q not allowed by policy: action %s", source.Identifier, decision.Action.String())
		}
		return ok, nil
	}
}

func mapsEqual[K comparable, V comparable](a, b map[K]V) error {
	if len(a) != len(b) {
		return errors.Errorf("map length mismatch: %d != %d", len(a), len(b))
	}
	for k, v := range a {
		vb, ok := b[k]
		if !ok {
			return errors.Errorf("key %v missing from second map", k)
		}
		if vb != v {
			return errors.Errorf("value mismatch for key %v: %v != %v", k, v, vb)
		}
	}
	return nil
}

func toPBPlatform(p *ocispecs.Platform) *pb.Platform {
	if p == nil {
		return nil
	}
	return &pb.Platform{
		Architecture: p.Architecture,
		OS:           p.OS,
		Variant:      p.Variant,
		OSVersion:    p.OSVersion,
		OSFeatures:   p.OSFeatures,
	}
}

func toOCIPlatform(p *pb.Platform) *ocispecs.Platform {
	if p == nil {
		return nil
	}
	return &ocispecs.Platform{
		Architecture: p.Architecture,
		OS:           p.OS,
		Variant:      p.Variant,
		OSVersion:    p.OSVersion,
		OSFeatures:   p.OSFeatures,
	}
}
