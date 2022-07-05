// Package sourcepolicyllbmutator provides the LLB mutator for applying the source policy.
package sourcepolicyllbmutator

import (
	"context"
	"fmt"
	"strings"

	"github.com/containerd/containerd/reference"
	"github.com/moby/buildkit/solver/llbsolver/llbmutator"
	"github.com/moby/buildkit/solver/pb"
	srctypes "github.com/moby/buildkit/source/types"
	"github.com/moby/buildkit/sourcepolicy"
	"github.com/moby/buildkit/util/bklog"
	binfotypes "github.com/moby/buildkit/util/buildinfo/types"
	"github.com/moby/buildkit/util/urlutil"
	digest "github.com/opencontainers/go-digest"
)

// New creates a new LLB mutator. May return nil when no mutation is needed.
func New(pol *sourcepolicy.SourcePolicy) (llbmutator.LLBMutator, error) {
	if pol == nil {
		return nil, nil
	}
	if len(pol.Sources) == 0 {
		return nil, nil
	}
	for i, f := range pol.Sources {
		switch f.Type {
		case binfotypes.SourceTypeDockerImage, binfotypes.SourceTypeHTTP, binfotypes.SourceTypeGit:
			// NOP
		default:
			return nil, fmt.Errorf("source policy: source %d: unknown type %q", i, f.Type)
		}
	}
	return &llbMutator{pol: pol}, nil
}

type llbMutator struct {
	pol *sourcepolicy.SourcePolicy
}

func (lm *llbMutator) Mutate(ctx context.Context, op *pb.Op) (bool, error) {
	srcOp := op.GetSource()
	if srcOp == nil {
		return false, nil
	}
	ident := srcOp.GetIdentifier()
	parts := strings.SplitN(ident, "://", 2)
	if len(parts) != 2 {
		return false, fmt.Errorf("failed to parse %q", ident)
	}
	switch parts[0] {
	case srctypes.DockerImageScheme:
		return lm.mutateDockerImage(ctx, srcOp, parts[0], parts[1])
	case srctypes.HTTPScheme, srctypes.HTTPSScheme:
		return lm.mutateHTTP(ctx, srcOp, parts[0], parts[1])
	case srctypes.GitScheme:
		// TODO
		return false, nil
	case srctypes.LocalScheme, srctypes.OCIScheme:
		// NOP
		return false, nil
	default:
		return false, fmt.Errorf("source policy applier: unknown source identifier type %q", ident)
	}
}

func (lm *llbMutator) mutateDockerImage(ctx context.Context, srcOp *pb.SourceOp, p0, p1 string) (bool, error) {
	var mutated bool
	ref, err := reference.Parse(p1)
	if err != nil {
		return mutated, err
	}
	// Discard the existing digest that might have been resolved by the Dockerfile frontend's MetaResolver.
	tag, _ := reference.SplitObject(ref.Object)
	tag = strings.TrimSuffix(tag, "@")
	if tag == "" {
		tag = "latest"
	}
	refQuery := ref.Locator + ":" + tag
	var hit *sourcepolicy.Source
	for _, f := range lm.pol.Sources {
		f := f
		if f.Type != binfotypes.SourceTypeDockerImage {
			continue
		}
		if f.Ref != refQuery {
			continue
		}
		if hit != nil {
			return mutated, fmt.Errorf("found multiple pin entries for %q", refQuery)
		}
		hit = &f
	}
	if hit != nil && hit.Pin != "" {
		newObj := tag + "@" + hit.Pin
		bklog.G(ctx).Debugf("source policy applier: pinning docker-image source %q: %q", refQuery, newObj)
		ref.Object = newObj
		srcOp.Identifier = p0 + "://" + ref.String()
		mutated = true
	}
	return mutated, nil
}

func (lm *llbMutator) mutateHTTP(ctx context.Context, srcOp *pb.SourceOp, p0, p1 string) (bool, error) {
	var mutated bool
	urlQuery := urlutil.RedactCredentials(p0 + "://" + p1)
	var hit *sourcepolicy.Source
	for _, f := range lm.pol.Sources {
		f := f
		if f.Type != binfotypes.SourceTypeHTTP {
			continue
		}
		if f.Ref != urlQuery {
			continue
		}
		if hit != nil {
			return mutated, fmt.Errorf("found multiple pin entries for %q", urlQuery)
		}
		hit = &f
	}
	if hit != nil && hit.Pin != "" {
		dgst, err := digest.Parse(hit.Pin)
		if err != nil {
			return mutated, err
		}
		bklog.G(ctx).Debugf("source policy applier: pinning http source %q: %q", urlQuery, dgst.String())
		if srcOp.Attrs == nil {
			srcOp.Attrs = make(map[string]string)
		}
		srcOp.Attrs[pb.AttrHTTPChecksum] = dgst.String()
		mutated = true
	}
	return mutated, nil
}
