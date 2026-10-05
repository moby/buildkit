package containerimage

import (
	"testing"

	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

func TestAnnotationsPlatformNormalized(t *testing.T) {
	ag, rest, err := ParseAnnotations(map[string][]byte{
		"annotation-manifest-descriptor[linux/arm64/v8].example": []byte("v"),
	})
	require.NoError(t, err)
	require.Empty(t, rest)

	for _, p := range []ocispecs.Platform{
		{OS: "linux", Architecture: "arm64"},
		{OS: "linux", Architecture: "arm64", Variant: "v8"},
	} {
		a := ag.Platform(&p)
		require.Equal(t, map[string]string{"example": "v"}, a.ManifestDescriptor, p)
	}

	a := ag.Platform(&ocispecs.Platform{OS: "linux", Architecture: "amd64"})
	require.Empty(t, a.ManifestDescriptor)

	k, ok, err := exptypes.ParseAnnotationKey("annotation-manifest[linux/arm64/v8].k")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "linux/arm64", k.PlatformString())
}
