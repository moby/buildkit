package containerimage

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/core/leases"
	"github.com/containerd/containerd/v2/core/remotes/docker"
	"github.com/containerd/containerd/v2/pkg/reference"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/buildkit/util/contentutil"
	"github.com/moby/buildkit/util/pull"
	"github.com/moby/buildkit/util/resolver"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

func TestNeverLayerContent(t *testing.T) {
	for _, present := range []bool{false, true} {
		name := "missing layer"
		if present {
			name = "local layer"
		}
		t.Run(name, func(t *testing.T) {
			ctx := leases.WithLease(t.Context(), "test")
			store := contentutil.NewBuffer()
			write := func(v any, mediaType string) ocispecs.Descriptor {
				t.Helper()
				dt, err := json.Marshal(v)
				require.NoError(t, err)
				desc := ocispecs.Descriptor{
					MediaType: mediaType,
					Digest:    digest.FromBytes(dt),
					Size:      int64(len(dt)),
				}
				require.NoError(t, content.WriteBlob(ctx, store, desc.Digest.String(), bytes.NewReader(dt), desc))
				return desc
			}
			layerData := []byte("local layer content")
			layer := ocispecs.Descriptor{
				MediaType: ocispecs.MediaTypeImageLayer,
				Digest:    digest.FromBytes(layerData),
				Size:      int64(len(layerData)),
			}
			if present {
				require.NoError(t, content.WriteBlob(ctx, store, layer.Digest.String(), bytes.NewReader(layerData), layer))
			}
			platform := ocispecs.Platform{OS: "linux", Architecture: "amd64"}
			config := write(ocispecs.Image{
				Platform: platform,
				RootFS: ocispecs.RootFS{
					Type:    "layers",
					DiffIDs: []digest.Digest{layer.Digest},
				},
			}, ocispecs.MediaTypeImageConfig)
			manifest := write(ocispecs.Manifest{
				MediaType: ocispecs.MediaTypeImageManifest,
				Config:    config,
				Layers:    []ocispecs.Descriptor{layer},
			}, ocispecs.MediaTypeImageManifest)
			const ref = "example.com/never:latest"
			src, err := reference.Parse(ref)
			require.NoError(t, err)
			p := &puller{
				Ref:        ref,
				Mode:       resolver.ResolveModeNever,
				ImageStore: &neverImageStore{target: manifest},
				RegistryHosts: func(string) ([]docker.RegistryHost, error) {
					t.Error("registry accessed in never mode")
					return nil, cerrdefs.ErrNotFound
				},
				Puller: &pull.Puller{
					ContentStore: store,
					Src:          src,
					Platform:     platform,
				},
			}
			_, _, _, _, err = p.CacheKey(ctx, nil, 0)
			if !present {
				require.ErrorIs(t, err, cerrdefs.ErrNotFound)
				require.ErrorContains(t, err, "pulling is disabled")
				return
			}
			require.NoError(t, err)
			handler := p.descHandlers[layer.Digest]
			require.NotNil(t, handler)
			require.Empty(t, handler.SnapshotLabels)
			dt, err := content.ReadBlob(ctx, handler.Provider(nil), layer)
			require.NoError(t, err)
			require.Equal(t, layerData, dt)
		})
	}
}

type neverImageStore struct {
	images.Store
	target ocispecs.Descriptor
}

func (s *neverImageStore) Get(context.Context, string) (images.Image, error) {
	return images.Image{Target: s.target}, nil
}
