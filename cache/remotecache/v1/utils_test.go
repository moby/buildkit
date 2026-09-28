package cacheimport

import (
	"testing"

	"github.com/moby/buildkit/solver"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

func TestMarshalRemoteConvertsSourceReference(t *testing.T) {
	dgst := digest.FromString("layer")
	remote := &solver.Remote{Descriptors: []ocispecs.Descriptor{{
		Digest:    dgst,
		MediaType: ocispecs.MediaTypeImageLayerGzip,
		Annotations: map[string]string{
			"containerd.io/distribution.source.ref": "registry.example.com/cache/app:main",
			"containerd.io/uncompressed":            digest.FromString("diff").String(),
		},
	}}}
	state := &marshalState{
		chainsByID:  map[string]int{},
		descriptors: DescriptorProvider{},
	}

	marshalRemote(t.Context(), remote, state)

	desc := state.descriptors[dgst].Descriptor
	require.NotContains(t, desc.Annotations, "containerd.io/distribution.source.ref")
	require.Equal(t, "cache/app", desc.Annotations["containerd.io/distribution.source.registry.example.com"])
	require.Equal(t, digest.FromString("diff").String(), desc.Annotations["containerd.io/uncompressed"])
	require.Equal(t, "registry.example.com/cache/app:main", remote.Descriptors[0].Annotations["containerd.io/distribution.source.ref"], "input descriptor must not be mutated")
	require.NotContains(t, remote.Descriptors[0].Annotations, "containerd.io/distribution.source.registry.example.com", "input descriptor must not be mutated")
}
