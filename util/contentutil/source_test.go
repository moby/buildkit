package contentutil

import (
	"testing"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/pkg/reference"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

func TestHasSource(t *testing.T) {
	info := content.Info{
		Labels: map[string]string{
			"containerd.io/distribution.source.docker.io": "library/alpine",
		},
	}
	ref, err := reference.Parse("docker.io/library/alpine:latest")
	require.NoError(t, err)
	b, err := HasSource(info, ref)
	require.NoError(t, err)
	require.True(t, b)

	info = content.Info{
		Labels: map[string]string{
			"containerd.io/distribution.source.docker.io": "library/alpine,library/ubuntu",
		},
	}
	b, err = HasSource(info, ref)
	require.NoError(t, err)
	require.True(t, b)

	info = content.Info{}
	b, err = HasSource(info, ref)
	require.NoError(t, err)
	require.False(t, b)

	info = content.Info{Labels: map[string]string{}}
	b, err = HasSource(info, ref)
	require.NoError(t, err)
	require.False(t, b)

	info = content.Info{
		Labels: map[string]string{
			"containerd.io/distribution.source.docker.io": "library/ubuntu",
		},
	}
	b, err = HasSource(info, ref)
	require.NoError(t, err)
	require.False(t, b)

	info = content.Info{Labels: map[string]string{
		"containerd.io/distribution.source.ghcr.io": "library/alpine",
	}}
	b, err = HasSource(info, ref)
	require.NoError(t, err)
	require.False(t, b)
}

func TestAddDistributionSource(t *testing.T) {
	desc := ocispecs.Descriptor{
		Annotations: map[string]string{
			"containerd.io/distribution.source.docker.io": "library/ubuntu",
			"containerd.io/distribution.source.ref":       "docker.io/library/alpine:latest",
		},
	}

	got, key, err := AddDistributionSource(desc, desc.Annotations["containerd.io/distribution.source.ref"])
	require.NoError(t, err)
	require.Equal(t, "containerd.io/distribution.source.docker.io", key)
	require.Equal(t, "library/ubuntu,library/alpine", got.Annotations[key])
	require.Equal(t, "library/ubuntu", desc.Annotations[key])

	got, _, err = AddDistributionSource(got, "docker.io/library/alpine:latest")
	require.NoError(t, err)
	require.Equal(t, "library/ubuntu,library/alpine", got.Annotations[key])
}
