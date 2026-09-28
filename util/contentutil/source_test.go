package contentutil

import (
	"testing"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/pkg/reference"
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

func TestAddSourceAnnotation(t *testing.T) {
	annotations := map[string]string{}
	key, err := AddSourceAnnotation(annotations, "registry.example.com:5000/cache/app:main")
	require.NoError(t, err)
	require.Equal(t, "containerd.io/distribution.source.registry.example.com", key)
	require.Equal(t, "cache/app", annotations[key])

	_, err = AddSourceAnnotation(annotations, "registry.example.com/cache/other:main")
	require.NoError(t, err)
	_, err = AddSourceAnnotation(annotations, "registry.example.com/cache/app:pr")
	require.NoError(t, err)
	require.Equal(t, "cache/app,cache/other", annotations[key], "repositories are appended once")

	info := content.Info{Labels: annotations}
	ref, err := reference.Parse("registry.example.com/cache/other:latest")
	require.NoError(t, err)
	b, err := HasSource(info, ref)
	require.NoError(t, err)
	require.True(t, b)

	_, err = AddSourceAnnotation(annotations, "")
	require.Error(t, err)
}
