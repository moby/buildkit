//go:build nydus

package client

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/containerd/nydus-snapshotter/pkg/converter"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/util/compression"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/moby/buildkit/util/testutil/workers"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
)

func init() {
	allTests = append(
		allTests,
		testBuildExportNydusWithHybrid,
		testNydusRegistryCacheExportKeepsRemoteLayerLazy,
	)
}

func testNydusRegistryCacheExportKeepsRemoteLayerLazy(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb,
		workers.FeatureCacheExport,
		workers.FeatureCacheImport,
		workers.FeatureCacheBackendRegistry,
		workers.FeatureDirectPush,
	)
	requiresLinux(t)

	cdAddress := sb.ContainerdAddress()
	if cdAddress == "" {
		t.Skip("test requires containerd worker")
	}

	ctd, err := newContainerd(cdAddress)
	require.NoError(t, err)
	defer ctd.Close()
	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	ctx := namespaces.WithNamespace(sb.Context(), "buildkit")
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	cacheRef := registry + "/nydus/lazy-cache:" + identity.NewID()
	imageRef := registry + "/nydus/lazy-image:" + identity.NewID()
	cacheExport := []CacheOptionsEntry{{
		Type: "registry",
		Attrs: map[string]string{
			"ref":               cacheRef,
			"mode":              "max",
			"compression":       "nydus",
			"force-compression": "true",
			"oci-mediatypes":    "true",
		},
	}}
	imageExport := []ExportEntry{{
		Type: ExporterImage,
		Attrs: map[string]string{
			"name":              imageRef,
			"push":              "true",
			"compression":       "nydus",
			"force-compression": "true",
			"oci-mediatypes":    "true",
		},
	}}

	base := llb.Scratch().File(llb.Mkfile("/dependency", 0644, bytes.Repeat([]byte("dependency"), 1<<16)))
	build := func(source string, imports []CacheOptionsEntry) {
		def, err := base.File(llb.Mkfile("/source", 0644, []byte(source))).Marshal(sb.Context())
		require.NoError(t, err)
		_, err = c.Solve(sb.Context(), def, SolveOpt{
			Exports:      imageExport,
			CacheExports: cacheExport,
			CacheImports: imports,
		}, nil)
		require.NoError(t, err)
	}

	build("first", nil)
	img, err := ctd.ImageService().Get(ctx, imageRef)
	require.NoError(t, err)
	manifest, err := images.Manifest(ctx, ctd.ContentStore(), img.Target, nil)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(manifest.Layers), 3)
	remoteBlob := manifest.Layers[len(manifest.Layers)-3]
	require.Equal(t, converter.MediaTypeNydusBlob, remoteBlob.MediaType)

	require.NoError(t, ctd.ImageService().Delete(ctx, imageRef, images.SynchronousDelete()))
	ensurePruneAll(t, c, sb)
	_, err = ctd.ContentStore().Info(ctx, remoteBlob.Digest)
	require.ErrorIs(t, err, cerrdefs.ErrNotFound)

	build("second", []CacheOptionsEntry{{Type: "registry", Attrs: map[string]string{"ref": cacheRef}}})
	_, err = ctd.ContentStore().Info(ctx, remoteBlob.Digest)
	require.ErrorIs(t, err, cerrdefs.ErrNotFound)
}

func testBuildExportNydusWithHybrid(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureDirectPush)
	requiresLinux(t)

	cdAddress := sb.ContainerdAddress()
	if cdAddress == "" {
		t.Skip("test requires containerd worker")
	}

	client, err := newContainerd(cdAddress)
	require.NoError(t, err)
	defer client.Close()
	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	var (
		imageService = client.ImageService()
		contentStore = client.ContentStore()
		ctx          = namespaces.WithNamespace(sb.Context(), "buildkit")
	)

	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	buildNydus := func(file string) {
		orgImage := "docker.io/library/alpine:latest"
		baseDef := llb.Image(orgImage).Run(llb.Args([]string{"/bin/touch", "/" + file}))
		def, err := baseDef.Marshal(sb.Context())
		require.NoError(t, err)

		target := registry + "/nydus/alpine:" + identity.NewID()
		_, err = c.Solve(sb.Context(), def, SolveOpt{
			Exports: []ExportEntry{
				{
					Type: ExporterImage,
					Attrs: map[string]string{
						"name":              target,
						"push":              "true",
						"compression":       "nydus",
						"oci-mediatypes":    "true",
						"force-compression": "true",
					},
				},
			},
		}, nil)
		require.NoError(t, err)

		img, err := imageService.Get(ctx, target)
		require.NoError(t, err)

		manifest, err := images.Manifest(ctx, contentStore, img.Target, nil)
		require.NoError(t, err)

		require.Len(t, manifest.Layers, 3)
		require.Equal(t, "true", manifest.Layers[0].Annotations[converter.LayerAnnotationNydusBlob])
		require.Equal(t, "true", manifest.Layers[1].Annotations[converter.LayerAnnotationNydusBlob])
		require.Equal(t, "true", manifest.Layers[2].Annotations[converter.LayerAnnotationNydusBootstrap])
	}

	buildOther := func(file string, compType compression.Type) {
		orgImage := "docker.io/library/alpine:latest"
		baseDef := llb.Image(orgImage).Run(llb.Args([]string{"/bin/touch", "/" + file}))
		def, err := baseDef.Marshal(sb.Context())
		require.NoError(t, err)

		mediaTypes := map[compression.Type]string{
			compression.Gzip: ocispecs.MediaTypeImageLayerGzip,
			compression.Zstd: ocispecs.MediaTypeImageLayerZstd,
		}
		target := fmt.Sprintf("%s/%s/alpine:%s", registry, compType, identity.NewID())
		_, err = c.Solve(sb.Context(), def, SolveOpt{
			Exports: []ExportEntry{
				{
					Type: ExporterImage,
					Attrs: map[string]string{
						"name":              target,
						"push":              "true",
						"compression":       compType.String(),
						"oci-mediatypes":    "true",
						"force-compression": "true",
					},
				},
			},
		}, nil)
		require.NoError(t, err)

		img, err := imageService.Get(ctx, target)
		require.NoError(t, err)

		manifest, err := images.Manifest(ctx, contentStore, img.Target, nil)
		require.NoError(t, err)

		require.Equal(t, 2, len(manifest.Layers))
		require.Equal(t, mediaTypes[compType], manifest.Layers[0].MediaType)
		require.Equal(t, mediaTypes[compType], manifest.Layers[1].MediaType)
	}

	// Make sure that the nydus compression layer is not mixed with other
	// types of compression layers in an image.
	buildNydus("foo")
	buildOther("foo", compression.Gzip)
	buildOther("foo", compression.Zstd)

	buildOther("bar", compression.Gzip)
	buildOther("bar", compression.Zstd)
	buildNydus("bar")
}
