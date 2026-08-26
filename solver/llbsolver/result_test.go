package llbsolver

import (
	"errors"
	"testing"

	"github.com/moby/buildkit/frontend"
	"github.com/moby/buildkit/solver/errdefs"
	"github.com/moby/buildkit/solver/pb"
	digest "github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
)

func TestProxyNetworkVertexErrorKeepsSourceLocation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		proxy bool
	}{
		{name: "disabled", proxy: false},
		{name: "enabled", proxy: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			def := proxyNetworkTestDefinition(t)
			definitionDigest := digest.FromBytes(def.Def[1])
			def.Source = &pb.Source{
				Infos: []*pb.SourceInfo{{Filename: "Dockerfile"}},
				Locations: map[string]*pb.Locations{
					definitionDigest.String(): {
						Locations: []*pb.Location{{SourceIndex: 0}},
					},
				},
			}

			digestMapping := map[digest.Digest]digest.Digest{}
			edge, err := loadWithProxyNetworkAndDigestMap(t.Context(), def, nil, tc.proxy, digestMapping)
			require.NoError(t, err)
			runtimeDigest := edge.Vertex.Digest()

			if tc.proxy {
				require.NotEqual(t, definitionDigest, runtimeDigest)
				require.Equal(t, runtimeDigest, digestMapping[definitionDigest])
			} else {
				require.Equal(t, definitionDigest, runtimeDigest)
				require.Empty(t, digestMapping)
			}

			rp := &resultProxy{
				req:           frontend.SolveRequest{Definition: def},
				digestMapping: digestMapping,
			}
			wrapped := rp.wrapError(errdefs.WrapVertex(errors.New("boom"), runtimeDigest))
			sources := errdefs.Sources(wrapped)
			require.Len(t, sources, 1)
			require.Equal(t, "Dockerfile", sources[0].Info.Filename)
		})
	}
}
