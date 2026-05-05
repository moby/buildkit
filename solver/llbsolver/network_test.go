package llbsolver

import (
	"testing"

	"github.com/moby/buildkit/sourcepolicy"
	spb "github.com/moby/buildkit/sourcepolicy/pb"
	"github.com/stretchr/testify/require"
)

func TestProxyPolicyRedactsCredentialsInErrors(t *testing.T) {
	p := &proxyPolicy{
		engine: sourcepolicy.NewEngine([]*spb.Policy{
			{
				Rules: []*spb.Rule{
					{
						Action: spb.PolicyAction_DENY,
						Selector: &spb.Selector{
							Identifier: "https://*",
						},
					},
				},
			},
		}),
	}

	err := p.CheckProxyRequest(t.Context(), "https://user:pass@example.com/path")
	require.ErrorIs(t, err, sourcepolicy.ErrSourceDenied)
	require.NotContains(t, err.Error(), "user")
	require.NotContains(t, err.Error(), "pass")
	require.Contains(t, err.Error(), "https://xxxxx:xxxxx@example.com/path")
}
