package sourcepolicy

import (
	"context"
	"strings"

	"github.com/moby/buildkit/solver/pb"
	spb "github.com/moby/buildkit/sourcepolicy/pb"
	"github.com/moby/buildkit/util/bklog"
	"github.com/pkg/errors"
)

var (
	// ErrSourceDenied is returned by the policy engine when a source is denied by the policy.
	ErrSourceDenied = errors.New("source denied by policy")

	// ErrTooManyOps is returned by the policy engine when there are too many converts for a single source op.
	ErrTooManyOps = errors.New("too many operations")
)

// Engine is the source policy engine.
// It is responsible for evaluating a source policy against a source operation.
// Create one with `NewEngine`
//
// Rule matching is delegated to the `Matcher` interface.
// Mutations are delegated to the `Mutater` interface.
type Engine struct {
	pol     []*spb.Policy
	sources map[string]*sourceCache
}

// NewEngine creates a new source policy engine.
func NewEngine(pol []*spb.Policy) *Engine {
	return &Engine{
		pol: pol,
	}
}

// TODO: The key here can't be used to cache attr constraint regexes.
func (e *Engine) sourceCache(src *spb.Source) *sourceCache {
	if e.sources == nil {
		e.sources = map[string]*sourceCache{}
	}

	key := src.MatchType.String() + " " + src.Type + "://" + src.Identifier

	if s, ok := e.sources[key]; ok {
		return s
	}
	s := &sourceCache{Source: src}

	e.sources[key] = s
	return s
}

// Evaluate evaluates a source operation against the policy.
//
// Policies are re-evaluated for each convert rule.
// Evaluate will error if the there are too many converts for a single source op to prevent infinite loops.
// This function may error out even if the op was mutated, in which case `true` will be returned along with the error.
//
// An error is returned when the source is denied by the policy.
func (e *Engine) Evaluate(ctx context.Context, op *pb.Op) (bool, error) {
	if len(e.pol) == 0 {
		return false, nil
	}

	var mutated bool
	const maxIterr = 20

	for i := 0; ; i++ {
		if i > maxIterr {
			return mutated, errors.Wrapf(ErrTooManyOps, "too many mutations on a single source")
		}

		srcOp := op.GetSource()
		if srcOp == nil {
			return false, nil
		}
		if i == 0 {
			ctx = bklog.WithLogger(ctx, bklog.G(ctx).WithField("orig", *srcOp).WithField("updated", op.GetSource()))
		}

		mut, err := e.evaluatePolicies(ctx, srcOp)
		if mut {
			mutated = true
		}
		if err != nil {
			return mutated, err
		}
		if !mut {
			break
		}
	}

	return mutated, nil
}

func (e *Engine) evaluatePolicies(ctx context.Context, srcOp *pb.SourceOp) (bool, error) {
	ident := srcOp.GetIdentifier()
	scheme, ref, found := strings.Cut(ident, "://")
	if !found || ref == "" {
		return false, errors.Errorf("failed to parse %q", ident)
	}

	ctx = bklog.WithLogger(ctx, bklog.G(ctx).WithFields(map[string]interface{}{
		"scheme": scheme,
		"ref":    ref,
	}))

	for _, pol := range e.pol {
		mut, err := e.evaluatePolicy(ctx, pol, srcOp, scheme, ref)
		if mut || err != nil {
			return mut, err
		}
	}
	return false, nil
}

func (e *Engine) evaluatePolicy(ctx context.Context, pol *spb.Policy, srcOp *pb.SourceOp, scheme, ref string) (bool, error) {
	for _, rule := range pol.Rules {
		mut, err := e.evaluateRule(ctx, rule, scheme, ref, srcOp)
		if mut || err != nil {
			return mut, err
		}
	}
	return false, nil
}

func (e *Engine) evaluateRule(ctx context.Context, rule *spb.Rule, scheme, ref string, op *pb.SourceOp) (bool, error) {
	origScheme := scheme
	var isHTTP bool
	switch scheme {
	case "http", "https":
		isHTTP = true
		// The scheme ref is important for http/https sources
		ref = scheme + "://" + ref

		// Update the scheme to match the rule
		// This is done so the rule can match regardless of what shceme we pulled off the URL and the rule can be written with either scheme.
		switch rule.Source.Type {
		case "http", "https":
			scheme = rule.Source.Type
		}
	}

	// get cached state for this source
	src := e.sourceCache(rule.Source)

	match, err := match(ctx, src, scheme, ref, op.Attrs)
	if err != nil {
		return false, errors.Wrap(err, "error evaluating rule")
	}

	bklog.G(ctx).Debug("sourcepolicy: rule match")

	if !match {
		return false, nil
	}

	switch rule.Action {
	case spb.PolicyAction_ALLOW:
		return false, nil
	case spb.PolicyAction_DENY:
		if match {
			return false, errors.Wrapf(ErrSourceDenied, "rule %s %s applies to source %s://%s", rule.Action, rule.Source.Identifier, scheme, ref)
		}
		return false, nil
	case spb.PolicyAction_CONVERT:
		if rule.Destination == nil {
			return false, errors.Errorf("missing destination for convert rule")
		}

		// TODO: This should really go in the mutator, but there's a lot of deatail we'd need to pass through.
		dest := rule.Destination.Identifier
		if dest == "" {
			dest = rule.Source.Identifier
		}
		dest, err = src.Format(ref, dest)
		if err != nil {
			return false, errors.Wrap(err, "error formatting destination")
		}

		typ := rule.Destination.Type
		if typ == "" {
			typ = origScheme
		}

		if !isHTTP {
			dest = typ + "://" + dest
		}

		bklog.G(ctx).Debugf("sourcepolicy: converting %s to %s, pattern: %s", ref, dest, rule.Destination.Identifier)

		return mutate(ctx, op, dest, rule.Destination.Attrs)
	default:
		return false, errors.Errorf("source policy: rule %s %s: unknown type %q", rule.Action, rule.Source.Identifier, ref)
	}
}