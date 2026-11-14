package cacheimport

import (
	"testing"

	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/util/contentutil"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

func TestLoadRemotesDistributionSources(t *testing.T) {
	remote := &solver.Remote{
		Descriptors: []ocispecs.Descriptor{{
			Annotations: map[string]string{
				contentutil.DistributionSourceRefAnnotation:   "docker.io/library/alpine:latest",
				"containerd.io/distribution.source.docker.io": "library/ubuntu",
				"example.com/other":                           "keep",
			},
		}},
	}
	id := remoteID(remote)
	it := &itemWithOutgoingLinks{
		item: &item{
			results: []solver.CacheExportResult{{Result: remote}},
		},
	}
	cs := &cacheResultStorage{
		byID:     map[string]*itemWithOutgoingLinks{"item": it},
		byResult: map[string]map[string]struct{}{id: {"item": {}}},
	}
	remotes, err := cs.LoadRemotes(t.Context(), solver.CacheResult{ID: id}, nil, session.NewGroup("test"))
	require.NoError(t, err)
	require.Len(t, remotes, 1)
	got := remotes[0].Descriptors[0]
	require.Equal(t, "library/ubuntu,library/alpine", got.Annotations["containerd.io/distribution.source.docker.io"])
	require.NotContains(t, got.Annotations, contentutil.DistributionSourceRefAnnotation)
	require.Equal(t, "keep", got.Annotations["example.com/other"])
	require.Equal(t, "docker.io/library/alpine:latest", remote.Descriptors[0].Annotations[contentutil.DistributionSourceRefAnnotation])
	require.Equal(t, "library/ubuntu", remote.Descriptors[0].Annotations["containerd.io/distribution.source.docker.io"])
}
