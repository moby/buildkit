package sourcepolicy

import (
	"context"
	"regexp"
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
	pol     *spb.Policy
	matcher Matcher
	mutater Mutater
	regexes map[string]*regexp.Regexp
}

// NewEngine creates a new source policy engine.
func NewEngine(pol *spb.Policy, matcher Matcher, mutater Mutater) *Engine {
	if matcher == nil {
		matcher = DefaultMatcher
	}
	if mutater == nil {
		mutater = DefaultMutater
	}
	return &Engine{
		pol:     pol,
		matcher: matcher,
		mutater: mutater,
	}
}

// Evaluate evaluates a source operation against the policy.
//
// Policies are re-evaluated for each convert rule.
// Evaluate will error if the there are too many converts for a single source op to prevent infinite loops.
// This function may error out even if the op was mutated, in which case `true` will be returned along with the error.
//
// An error is returned when the source is denied by the policy.
func (e *Engine) Evaluate(ctx context.Context, op *pb.Op) (bool, error) {
	if len(e.pol.Rules) == 0 {
		return false, nil
	}

	var (
		st      evalState
		mutated bool
	)
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

		mut, err := e.evaluateRules(ctx, srcOp, &st)
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

func (e *Engine) evaluateRules(ctx context.Context, srcOp *pb.SourceOp, st *evalState) (bool, error) {
	ident := srcOp.GetIdentifier()
	scheme, ref, found := strings.Cut(ident, "://")
	if !found || ref == "" {
		return false, errors.Errorf("failed to parse %q", ident)
	}

	ctx = bklog.WithLogger(ctx, bklog.G(ctx).WithFields(map[string]interface{}{
		"scheme": scheme,
		"ref":    ref,
	}))

	for _, rule := range e.pol.Rules {
		mut, err := e.evaluateRule(ctx, rule, scheme, ref, srcOp, st)
		if err != nil {
			return false, err
		}
		if mut {
			return true, nil
		}
	}
	return false, nil
}

// temporary state while evaluating a policy

func (e *Engine) regex(key, pattern string) (*regexp.Regexp, error) {
	if e.regexes == nil {
		e.regexes = map[string]*regexp.Regexp{}
	}
	if r, ok := e.regexes[key]; ok {
		return r, nil
	}
	r, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	e.regexes[key] = r
	return r, nil
}

func (e *Engine) evaluateRule(ctx context.Context, rule *spb.Rule, scheme, ref string, op *pb.SourceOp, st *evalState) (bool, error) {
	switch scheme {
	case "http", "https":
		// The scheme ref is important for http/https sources
		ref = scheme + "://" + ref

		// Update the scheme to match the rule
		// This is done so the rule can match regardless of what shceme we pulled off the URL and the rule can be written with either scheme.
		switch rule.Source.Type {
		case "http", "https":
			scheme = rule.Source.Type
		}
	}

	var regex *regexp.Regexp
	switch rule.Source.MatchType {
	case spb.MatchType_EXACT:
	case spb.MatchType_REGEX:
		var err error
		regex, err = e.regex(rule.Source.Type+"://"+rule.Source.Identifier, rule.Source.Identifier)
		if err != nil {
			return false, err
		}
	}

	match, err := e.matcher.Match(ctx, rule.Source, scheme, ref, op.Attrs, regex)
	if err != nil {
		return false, errors.Wrap(err, "error evaluating rule")
	}

	bklog.G(ctx).Debug("sourcepolicy: rule match")

	if !match {
		return false, nil
	}

	switch rule.Action {
	case spb.PolicyAction_ALLOW:
		st.allow(ref)
		return false, nil
	case spb.PolicyAction_DENY:
		// If this has already been allowed by a previous rule then we can ignore it.
		if st.allowed[ref] {
			return false, nil
		}
		if match {
			return false, errors.Wrapf(ErrSourceDenied, "rule %s %s applies to source %s://%s", rule.Action, rule.Source.Identifier, scheme, ref)
		}
		return false, nil
	case spb.PolicyAction_CONVERT:
		dest := rule.Destination.Identifier
		if regex != nil {
			dest = regex.ReplaceAllString(ref, dest)
		}
		return e.mutater.Mutate(ctx, op, dest, rule.Destination.Attrs)
	default:
		return false, errors.Errorf("source policy: rule %s %s: unknown type %q", rule.Action, rule.Source.Identifier, ref)
	}
}

type evalState struct {
	allowed map[string]bool
}

func (st *evalState) allow(ref string) {
	if st.allowed == nil {
		st.allowed = map[string]bool{}
	}
	st.allowed[ref] = true
}
