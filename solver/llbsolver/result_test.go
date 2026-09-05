package llbsolver

import (
	"testing"

	"github.com/moby/buildkit/frontend"
	"github.com/moby/buildkit/solver/errdefs"
	"github.com/moby/buildkit/solver/pb"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestWrapErrorSourceLocations(t *testing.T) {
	dgst := digest.FromString("vertex")
	sourceWithLocations := func(locs *pb.Locations) *pb.Source {
		return &pb.Source{
			Infos: []*pb.SourceInfo{{Filename: "Dockerfile"}},
			Locations: map[string]*pb.Locations{
				string(dgst): locs,
			},
		}
	}

	for _, tc := range []struct {
		name       string
		source     *pb.Source
		wantSource bool
	}{
		{
			name:       "valid location",
			source:     sourceWithLocations(&pb.Locations{Locations: []*pb.Location{{SourceIndex: 0}}}),
			wantSource: true,
		},
		{
			name: "nil source",
		},
		{
			name:   "missing locations",
			source: &pb.Source{},
		},
		{
			name:   "nil locations",
			source: sourceWithLocations(nil),
		},
		{
			name:   "nil location",
			source: sourceWithLocations(&pb.Locations{Locations: []*pb.Location{nil}}),
		},
		{
			name:   "negative source index",
			source: sourceWithLocations(&pb.Locations{Locations: []*pb.Location{{SourceIndex: -1}}}),
		},
		{
			name:   "source index out of range",
			source: sourceWithLocations(&pb.Locations{Locations: []*pb.Location{{SourceIndex: 1}}}),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			baseErr := errors.New("build failed")
			err := errdefs.WrapVertex(baseErr, dgst)
			rp := &resultProxy{req: frontend.SolveRequest{
				Definition: &pb.Definition{Source: tc.source},
			}}

			wrapped := rp.wrapError(err)
			require.ErrorIs(t, wrapped, baseErr)
			sources := errdefs.Sources(wrapped)
			if tc.wantSource {
				require.Len(t, sources, 1)
				require.Equal(t, "Dockerfile", sources[0].Info.Filename)
			} else {
				require.Empty(t, sources)
			}
		})
	}
}
