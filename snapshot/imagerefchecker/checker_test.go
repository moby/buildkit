package imagerefchecker

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/plugins/content/local"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

func TestExists(t *testing.T) {
	ctx := t.Context()

	cs, err := local.NewStore(t.TempDir())
	require.NoError(t, err)

	layer1 := digest.FromString("layer1")
	layer2 := digest.FromString("layer2")

	dt, err := json.Marshal(ocispecs.Manifest{
		MediaType: ocispecs.MediaTypeImageManifest,
		Layers: []ocispecs.Descriptor{
			{MediaType: ocispecs.MediaTypeImageLayerGzip, Digest: layer1},
			{MediaType: ocispecs.MediaTypeImageLayerGzip, Digest: layer2},
		},
	})
	require.NoError(t, err)
	desc := ocispecs.Descriptor{
		MediaType: ocispecs.MediaTypeImageManifest,
		Digest:    digest.FromBytes(dt),
		Size:      int64(len(dt)),
	}
	require.NoError(t, content.WriteBlob(ctx, cs, "manifest", bytes.NewReader(dt), desc))

	c, err := New(Opt{
		ImageStore:   &imageStore{imgs: []images.Image{{Name: "base", Target: desc}}},
		ContentStore: cs,
	})()
	require.NoError(t, err)

	require.True(t, c.Exists("same", []digest.Digest{layer1, layer2}))
	require.False(t, c.Exists("prefix", []digest.Digest{layer1}))

	// A record without a blob has an empty digest at the end of its chain. It
	// was never exported, so no image can reference it, even though its chain
	// without the empty digest is the same as the image.
	require.False(t, c.Exists("noblob", []digest.Digest{layer1, layer2, ""}))
	require.False(t, c.Exists("noblob-nested", []digest.Digest{layer1, layer2, "", ""}))
}

type imageStore struct {
	images.Store
	imgs []images.Image
}

func (s *imageStore) List(context.Context, ...string) ([]images.Image, error) {
	return s.imgs, nil
}
