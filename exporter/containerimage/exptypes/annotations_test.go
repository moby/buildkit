package exptypes

import (
	"testing"

	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

func TestParseAnnotationKeyPlatform(t *testing.T) {
	for _, tc := range []struct {
		platform string
		want     ocispecs.Platform
	}{
		{"linux/arm64", ocispecs.Platform{OS: "linux", Architecture: "arm64"}},
		{"linux/arm64/v8", ocispecs.Platform{OS: "linux", Architecture: "arm64"}},
		{"linux/arm64/8", ocispecs.Platform{OS: "linux", Architecture: "arm64"}},
		{"linux/arm64/v9", ocispecs.Platform{OS: "linux", Architecture: "arm64", Variant: "v9"}},
		{"linux/arm", ocispecs.Platform{OS: "linux", Architecture: "arm", Variant: "v7"}},
		{"linux/arm/v6", ocispecs.Platform{OS: "linux", Architecture: "arm", Variant: "v6"}},
		{"linux/arm/v7", ocispecs.Platform{OS: "linux", Architecture: "arm", Variant: "v7"}},
	} {
		for _, typ := range []string{AnnotationManifest, AnnotationManifestDescriptor} {
			t.Run(typ+"/"+tc.platform, func(t *testing.T) {
				key, ok, err := ParseAnnotationKey("annotation-" + typ + "[" + tc.platform + "].example")
				require.NoError(t, err)
				require.True(t, ok)
				require.Equal(t, AnnotationKey{Type: typ, Platform: &tc.want, Key: "example"}, key)
			})
		}
	}
}
