//go:build nydus

package compression

import (
	"testing"

	"github.com/containerd/nydus-snapshotter/pkg/converter"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

func TestNydusNeedsConversionWithoutLocalBlob(t *testing.T) {
	for _, tc := range []struct {
		name string
		desc ocispecs.Descriptor
		want bool
	}{
		{
			name: "remote nydus blob",
			desc: ocispecs.Descriptor{
				MediaType:   converter.MediaTypeNydusBlob,
				Annotations: map[string]string{converter.LayerAnnotationNydusBlob: "true"},
			},
		},
		{
			name: "nydus layer without annotation",
			desc: ocispecs.Descriptor{MediaType: converter.MediaTypeNydusBlob},
			want: true,
		},
		{
			name: "gzip layer with nydus annotation",
			desc: ocispecs.Descriptor{
				MediaType:   ocispecs.MediaTypeImageLayerGzip,
				Annotations: map[string]string{converter.LayerAnnotationNydusBlob: "true"},
			},
			want: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Nydus.NeedsConversion(t.Context(), nil, tc.desc)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}
