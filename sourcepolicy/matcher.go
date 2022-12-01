package sourcepolicy

import (
	"context"
	"regexp"
	"strings"

	sourcetypes "github.com/moby/buildkit/source/types"
	spb "github.com/moby/buildkit/sourcepolicy/pb"
	"github.com/pkg/errors"
)

var DefaultMatcher = MatcherFn(Match)

// Match returns true if the given ref matches the given pattern.
type Matcher interface {
	Match(ctx context.Context, src *Source, scheme, ref string, attrs map[string]string) (bool, error)
}

// MatcherFn can be used to wrap a function as a Matcher.
type MatcherFn func(ctx context.Context, src *Source, scheme, ref string, attrs map[string]string) (bool, error)

// Match runs the matcher function.
// Match implements the Matcher interface for MatcherFn
func (f MatcherFn) Match(ctx context.Context, src *Source, scheme, ref string, attrs map[string]string) (bool, error) {
	return f(ctx, src, scheme, ref, attrs)
}

type PatternMatcher interface {
	MatchString(string) (bool, error)
}

// Match is a MatcherFn which matches vthe source operation to the identifier and attributes provided by the policy.
func Match(ctx context.Context, src *Source, scheme, ref string, attrs map[string]string) (bool, error) {
	if src.Type != scheme {
		return false, nil
	}

	for _, c := range src.Constraints {
		switch c.Condition {
		case spb.AttrMatch_EQUAL:
			if attrs[c.Key] != c.Value {
				return false, nil
			}
		case spb.AttrMatch_NOTEQUAL:
			if attrs[c.Key] == c.Value {
				return false, nil
			}
		case spb.AttrMatch_MATCHES:
			// TODO: Cache the compiled regex
			matches, err := regexp.MatchString(c.Value, attrs[c.Key])
			if err != nil {
				return false, errors.Errorf("invalid regex %q: %v", c.Value, err)
			}
			if !matches {
				return false, nil
			}
		default:
			return false, errors.Errorf("unknown attr condition: %s", c.Condition)
		}
	}

	if src.Identifier == ref {
		return true, nil
	}

	if src.Type == sourcetypes.DockerImageScheme {
		ref, _, _ = strings.Cut(ref, "@")
		if src.Identifier == ref {
			return true, nil
		}
	}

	switch src.MatchType {
	case spb.MatchType_EXACT:
		return false, nil
	case spb.MatchType_REGEX:
		re, err := src.regex()
		if err != nil {
			return false, err
		}
		return re.MatchString(ref), nil
	case spb.MatchType_WILDCARD:
		w, err := src.wildcard()
		if err != nil {
			return false, err
		}
		return w.Match(ref) != nil, nil
	default:
		return false, errors.Errorf("unknown match type: %s", src.MatchType)
	}
}
