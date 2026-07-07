package client

import (
	"archive/tar"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	ctd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/content/proxy"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/core/remotes/docker"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/containerd/v2/plugins/content/local"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/containerd/platforms"
	controlapi "github.com/moby/buildkit/api/services/control"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/client/llb/sourceresolver"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/contentutil"
	"github.com/moby/buildkit/util/testutil"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/moby/buildkit/util/testutil/workers"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

func testBuildExportScratch(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureImageExporter)
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	makeFrontend := func(ps []string) func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		return func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
			st := llb.Scratch()
			def, err := st.Marshal(sb.Context())
			require.NoError(t, err)

			r, err := c.Solve(ctx, gateway.SolveRequest{
				Definition: def.ToPB(),
			})
			if err != nil {
				return nil, err
			}

			ref, err := r.SingleRef()
			if err != nil {
				return nil, err
			}

			res := gateway.NewResult()
			if ps == nil {
				res.SetRef(ref)
			} else {
				for _, p := range ps {
					res.AddRef(p, ref)
				}

				expPlatforms := &exptypes.Platforms{
					Platforms: make([]exptypes.Platform, len(ps)),
				}
				for i, pk := range ps {
					p := platforms.MustParse(pk)

					img := ocispecs.Image{
						Platform: p,
						Config: ocispecs.ImageConfig{
							Labels: map[string]string{
								"foo": "i am platform " + platforms.Format(p),
							},
						},
					}
					config, err := json.Marshal(img)
					if err != nil {
						return nil, errors.Wrapf(err, "failed to marshal image config")
					}
					res.AddMeta(fmt.Sprintf("%s/%s", exptypes.ExporterImageConfigKey, pk), config)

					expPlatforms.Platforms[i] = exptypes.Platform{
						ID:       pk,
						Platform: p,
					}
				}
				dt, err := json.Marshal(expPlatforms)
				if err != nil {
					return nil, err
				}
				res.AddMeta(exptypes.ExporterPlatformsKey, dt)
			}

			return res, nil
		}
	}

	target := registry + "/buildkit/build/exporter:withnocompressed"

	_, err = c.Build(sb.Context(), SolveOpt{
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name":              target,
					"push":              "true",
					"unpack":            "true",
					"compression":       "uncompressed",
					"attest:provenance": "mode=max",
				},
			},
		},
	}, "", makeFrontend(nil), nil)
	require.NoError(t, err)

	targetMulti := registry + "/buildkit/build/exporter-multi:withnocompressed"
	_, err = c.Build(sb.Context(), SolveOpt{
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name":              targetMulti,
					"push":              "true",
					"unpack":            "true",
					"compression":       "uncompressed",
					"attest:provenance": "mode=max",
				},
			},
		},
	}, "", makeFrontend([]string{"linux/amd64", "linux/arm64"}), nil)
	require.NoError(t, err)

	desc, provider, err := contentutil.ProviderFromRef(target)
	require.NoError(t, err)
	imgs, err := testutil.ReadImages(sb.Context(), provider, desc)
	require.NoError(t, err)
	require.Len(t, imgs.Images, 1)
	img := imgs.Images[0]
	require.Empty(t, img.Layers)
	require.True(t, platforms.Only(platforms.DefaultSpec()).Match(img.Img.Platform))

	desc, provider, err = contentutil.ProviderFromRef(targetMulti)
	require.NoError(t, err)
	imgs, err = testutil.ReadImages(sb.Context(), provider, desc)
	require.NoError(t, err)
	require.Len(t, imgs.Images, 2)
	img = imgs.Find("linux/amd64")
	require.Empty(t, img.Layers)
	require.Equal(t, "linux/amd64", platforms.Format(img.Img.Platform))
	require.Equal(t, "i am platform linux/amd64", img.Img.Config.Labels["foo"])
	img = imgs.Find("linux/arm64")
	require.Empty(t, img.Layers)
	require.Equal(t, "linux/arm64", platforms.Format(img.Img.Platform))
	require.Equal(t, "i am platform linux/arm64", img.Img.Config.Labels["foo"])
}

// testBuildExportWithForeignLayer verifies foreign (non-distributable) layer handling during
// image export. propagate=1 preserves foreign media type and URLs; propagate=0 converts to
// regular layers. Skipped on Windows: the test image is Linux-only and BuildKit's Windows
// exporter does not preserve foreign layer media types.
func testBuildExportWithForeignLayer(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows", "test image is Linux-only and BuildKit's Windows exporter strips foreign layer media types")
	workers.CheckFeatureCompat(t, sb, workers.FeatureImageExporter)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	st := llb.Image("cpuguy83/buildkit-foreign:latest")
	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	t.Run("propagate=1", func(t *testing.T) {
		registry, err := sb.NewRegistry()
		if errors.Is(err, integration.ErrRequirements) {
			t.Skip(err.Error())
		}
		require.NoError(t, err)

		target := registry + "/buildkit/build/exporter/foreign:latest"
		_, err = c.Solve(sb.Context(), def, SolveOpt{
			Exports: []ExportEntry{
				{
					Type: ExporterImage,
					Attrs: map[string]string{
						"name":                  target,
						"push":                  "true",
						"oci-mediatypes":        "false",
						"prefer-nondist-layers": "true",
					},
				},
			},
		}, nil)
		require.NoError(t, err)

		ctx := namespaces.WithNamespace(sb.Context(), "buildkit")

		resolver := docker.NewResolver(docker.ResolverOptions{PlainHTTP: true})
		name, desc, err := resolver.Resolve(ctx, target)
		require.NoError(t, err)

		fetcher, err := resolver.Fetcher(ctx, name)
		require.NoError(t, err)
		mfst, err := images.Manifest(ctx, contentutil.FromFetcher(fetcher), desc, platforms.Any())
		require.NoError(t, err)

		require.Equal(t, 2, len(mfst.Layers))
		require.Equal(t, images.MediaTypeDockerSchema2LayerForeign, mfst.Layers[0].MediaType)
		require.Len(t, mfst.Layers[0].URLs, 1)
		require.Equal(t, images.MediaTypeDockerSchema2Layer, mfst.Layers[1].MediaType)

		rc, err := fetcher.Fetch(ctx, ocispecs.Descriptor{Digest: mfst.Layers[0].Digest, Size: mfst.Layers[0].Size})
		require.NoError(t, err)
		defer rc.Close()

		// `Fetch` doesn't error (in the docker resolver), it just returns a reader immediately and does not make a request.
		// The request is only made when we attempt to read from the reader.
		buf := make([]byte, 1)
		_, err = rc.Read(buf)
		require.Truef(t, cerrdefs.IsNotFound(err), "expected error for blob that should not be in registry: %s, %v", mfst.Layers[0].Digest, err)
	})
	t.Run("propagate=0", func(t *testing.T) {
		registry, err := sb.NewRegistry()
		if errors.Is(err, integration.ErrRequirements) {
			t.Skip(err.Error())
		}
		require.NoError(t, err)
		target := registry + "/buildkit/build/exporter/noforeign:latest"
		_, err = c.Solve(sb.Context(), def, SolveOpt{
			Exports: []ExportEntry{
				{
					Type: ExporterImage,
					Attrs: map[string]string{
						"name":           target,
						"push":           "true",
						"oci-mediatypes": "false",
					},
				},
			},
		}, nil)
		require.NoError(t, err)

		ctx := namespaces.WithNamespace(sb.Context(), "buildkit")

		resolver := docker.NewResolver(docker.ResolverOptions{PlainHTTP: true})
		name, desc, err := resolver.Resolve(ctx, target)
		require.NoError(t, err)

		fetcher, err := resolver.Fetcher(ctx, name)
		require.NoError(t, err)

		mfst, err := images.Manifest(ctx, contentutil.FromFetcher(fetcher), desc, platforms.Any())
		require.NoError(t, err)

		require.Equal(t, 2, len(mfst.Layers))
		require.Equal(t, images.MediaTypeDockerSchema2Layer, mfst.Layers[0].MediaType)
		require.Len(t, mfst.Layers[0].URLs, 0)
		require.Equal(t, images.MediaTypeDockerSchema2Layer, mfst.Layers[1].MediaType)

		rc, err := fetcher.Fetch(ctx, ocispecs.Descriptor{Digest: mfst.Layers[0].Digest, Size: mfst.Layers[0].Size})
		require.NoError(t, err)
		defer rc.Close()

		// `Fetch` doesn't error (in the docker resolver), it just returns a reader immediately and does not make a request.
		// The request is only made when we attempt to read from the reader.
		buf := make([]byte, 1)
		_, err = rc.Read(buf)
		require.NoError(t, err)
	})
}

func testBuildExportWithUncompressed(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureImageExporter)
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	busybox := llb.Image("busybox:latest")
	cmd := `sh -e -c "echo -n uncompressed > data"`

	st := llb.Scratch()
	st = busybox.Run(llb.Shlex(cmd), llb.Dir("/wd")).AddMount("/wd", st)

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	target := registry + "/buildkit/build/exporter:withnocompressed"

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name":        target,
					"push":        "true",
					"compression": "uncompressed",
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	ctx := namespaces.WithNamespace(sb.Context(), "buildkit")
	cdAddress := sb.ContainerdAddress()
	var client *ctd.Client
	if cdAddress != "" {
		client, err = newContainerd(cdAddress)
		require.NoError(t, err)
		defer client.Close()

		img, err := client.GetImage(ctx, target)
		require.NoError(t, err)
		mfst, err := images.Manifest(ctx, client.ContentStore(), img.Target(), nil)
		require.NoError(t, err)
		require.Equal(t, 1, len(mfst.Layers))
		require.Equal(t, ocispecs.MediaTypeImageLayer, mfst.Layers[0].MediaType)
	}

	// new layer with gzip compression
	targetImg := llb.Image(target)
	cmd = `sh -e -c "echo -n gzip > data"`
	st = busybox.Run(llb.Shlex(cmd), llb.Dir("/wd")).AddMount("/wd", targetImg)

	def, err = st.Marshal(sb.Context())
	require.NoError(t, err)

	compressedTarget := registry + "/buildkit/build/exporter:withcompressed"
	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name": compressedTarget,
					"push": "true",
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	allCompressedTarget := registry + "/buildkit/build/exporter:withallcompressed"
	_, err = c.Solve(context.TODO(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name":              allCompressedTarget,
					"push":              "true",
					"compression":       "gzip",
					"force-compression": "true",
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	if cdAddress == "" {
		t.Skip("rest of test requires containerd worker")
	}

	err = client.ImageService().Delete(ctx, target, images.SynchronousDelete())
	require.NoError(t, err)
	err = client.ImageService().Delete(ctx, compressedTarget, images.SynchronousDelete())
	require.NoError(t, err)
	err = client.ImageService().Delete(ctx, allCompressedTarget, images.SynchronousDelete())
	require.NoError(t, err)

	checkAllReleasable(t, c, sb, true)

	// check if the new layer is compressed with compression option
	img, err := client.Pull(ctx, compressedTarget)
	require.NoError(t, err)

	dt, err := content.ReadBlob(ctx, img.ContentStore(), img.Target())
	require.NoError(t, err)

	mfst := struct {
		MediaType string `json:"mediaType,omitempty"`
		ocispecs.Manifest
	}{}

	err = json.Unmarshal(dt, &mfst)
	require.NoError(t, err)
	require.Equal(t, 2, len(mfst.Layers))
	require.Equal(t, ocispecs.MediaTypeImageLayer, mfst.Layers[0].MediaType)
	require.Equal(t, ocispecs.MediaTypeImageLayerGzip, mfst.Layers[1].MediaType)

	dt, err = content.ReadBlob(ctx, img.ContentStore(), ocispecs.Descriptor{Digest: mfst.Layers[0].Digest})
	require.NoError(t, err)

	m, err := testutil.ReadTarToMap(dt, false)
	require.NoError(t, err)

	item, ok := m["data"]
	require.True(t, ok)
	require.Equal(t, tar.TypeReg, int32(item.Header.Typeflag))
	require.Equal(t, []byte("uncompressed"), item.Data)

	dt, err = content.ReadBlob(ctx, img.ContentStore(), ocispecs.Descriptor{Digest: mfst.Layers[1].Digest})
	require.NoError(t, err)

	m, err = testutil.ReadTarToMap(dt, true)
	require.NoError(t, err)

	item, ok = m["data"]
	require.True(t, ok)
	require.Equal(t, tar.TypeReg, int32(item.Header.Typeflag))
	require.Equal(t, []byte("gzip"), item.Data)

	err = client.ImageService().Delete(ctx, compressedTarget, images.SynchronousDelete())
	require.NoError(t, err)

	checkAllReleasable(t, c, sb, true)

	// check if all layers are compressed with force-compressoin option
	img, err = client.Pull(ctx, allCompressedTarget)
	require.NoError(t, err)

	dt, err = content.ReadBlob(ctx, img.ContentStore(), img.Target())
	require.NoError(t, err)

	mfst = struct {
		MediaType string `json:"mediaType,omitempty"`
		ocispecs.Manifest
	}{}

	err = json.Unmarshal(dt, &mfst)
	require.NoError(t, err)
	require.Equal(t, 2, len(mfst.Layers))
	require.Equal(t, ocispecs.MediaTypeImageLayerGzip, mfst.Layers[0].MediaType)
	require.Equal(t, ocispecs.MediaTypeImageLayerGzip, mfst.Layers[1].MediaType)

	dt, err = content.ReadBlob(ctx, img.ContentStore(), ocispecs.Descriptor{Digest: mfst.Layers[0].Digest})
	require.NoError(t, err)

	m, err = testutil.ReadTarToMap(dt, true)
	require.NoError(t, err)

	item, ok = m["data"]
	require.True(t, ok)
	require.Equal(t, tar.TypeReg, int32(item.Header.Typeflag))
	require.Equal(t, []byte("uncompressed"), item.Data)

	dt, err = content.ReadBlob(ctx, img.ContentStore(), ocispecs.Descriptor{Digest: mfst.Layers[1].Digest})
	require.NoError(t, err)

	m, err = testutil.ReadTarToMap(dt, true)
	require.NoError(t, err)

	item, ok = m["data"]
	require.True(t, ok)
	require.Equal(t, tar.TypeReg, int32(item.Header.Typeflag))
	require.Equal(t, []byte("gzip"), item.Data)
}

// testBuildExportZstd verifies OCI export with Zstd-compressed layers.
// It builds a simple layer, exports it with Zstd compression, and validates both
// OCI and Docker schema 2 media types.
func testBuildExportZstd(t *testing.T, sb integration.Sandbox) {
	// Skipped on Windows:
	// 1. AddMount() fails because Windows snapshots allow only a single mount per layer.
	// 2. OCI export fails because windowsLcowDiff lacks the Compare method needed to
	//    generate compressed layer diffs.
	integration.SkipOnPlatform(t, "windows", "Windows container support incomplete: AddMount() fails with 'number of mounts should always be 1 for Windows layers', OCI export fails with 'windowsLcowDiff does not implement Compare method'")
	workers.CheckFeatureCompat(t, sb, workers.FeatureOCIExporter)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	busybox := llb.Image("busybox:latest")
	cmd := `sh -e -c "echo -n zstd > data"`

	st := llb.Scratch()
	st = busybox.Run(llb.Shlex(cmd), llb.Dir("/wd")).AddMount("/wd", st)

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	destDir := t.TempDir()

	out := filepath.Join(destDir, "out.tar")
	outW, err := os.Create(out)
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:   ExporterOCI,
				Output: fixedWriteCloser(outW),
				Attrs: map[string]string{
					"compression": "zstd",
				},
			},
		},
		// compression option should work even with inline cache exports
		CacheExports: []CacheOptionsEntry{
			{
				Type: "inline",
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(out)
	require.NoError(t, err)

	m, err := testutil.ReadTarToMap(dt, false)
	require.NoError(t, err)

	var index ocispecs.Index
	err = json.Unmarshal(m[ocispecs.ImageIndexFile].Data, &index)
	require.NoError(t, err)

	var mfst ocispecs.Manifest
	err = json.Unmarshal(m[ocispecs.ImageBlobsDir+"/sha256/"+index.Manifests[0].Digest.Hex()].Data, &mfst)
	require.NoError(t, err)

	lastLayer := mfst.Layers[len(mfst.Layers)-1]
	require.Equal(t, ocispecs.MediaTypeImageLayerZstd, lastLayer.MediaType)

	zstdLayerDigest := lastLayer.Digest.Hex()
	require.Equal(t, []byte{0x28, 0xb5, 0x2f, 0xfd}, m[ocispecs.ImageBlobsDir+"/sha256/"+zstdLayerDigest].Data[:4])

	// repeat without oci mediatype
	outW, err = os.Create(out)
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:   ExporterOCI,
				Output: fixedWriteCloser(outW),
				Attrs: map[string]string{
					"compression":    "zstd",
					"oci-mediatypes": "false",
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err = os.ReadFile(out)
	require.NoError(t, err)

	m, err = testutil.ReadTarToMap(dt, false)
	require.NoError(t, err)

	err = json.Unmarshal(m[ocispecs.ImageIndexFile].Data, &index)
	require.NoError(t, err)

	err = json.Unmarshal(m[ocispecs.ImageBlobsDir+"/sha256/"+index.Manifests[0].Digest.Hex()].Data, &mfst)
	require.NoError(t, err)

	lastLayer = mfst.Layers[len(mfst.Layers)-1]
	require.Equal(t, images.MediaTypeDockerSchema2LayerZstd, lastLayer.MediaType)

	require.Equal(t, lastLayer.Digest.Hex(), zstdLayerDigest)
}

func testBuildPushAndValidate(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureDirectPush)
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	busybox := llb.Image("busybox:latest")
	st := llb.Scratch()

	run := func(cmd string) {
		st = busybox.Run(llb.Shlex(cmd), llb.Dir("/wd")).AddMount("/wd", st)
	}

	run(`sh -e -c "mkdir -p foo/sub; echo -n first > foo/sub/bar; chmod 0741 foo;"`)
	run(`true`) // this doesn't create a layer
	run(`sh -c "echo -n second > foo/sub/baz"`)

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	target := registry + "/buildkit/testpush:latest"

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name": target,
					"push": "true",
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	// test existence of the image with next build
	firstBuild := llb.Image(target)

	def, err = firstBuild.Marshal(sb.Context())
	require.NoError(t, err)

	destDir := t.TempDir()

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "foo/sub/bar"))
	require.NoError(t, err)
	require.Equal(t, dt, []byte("first"))

	dt, err = os.ReadFile(filepath.Join(destDir, "foo/sub/baz"))
	require.NoError(t, err)
	require.Equal(t, dt, []byte("second"))

	fi, err := os.Stat(filepath.Join(destDir, "foo"))
	require.NoError(t, err)
	require.Equal(t, 0741, int(fi.Mode()&0777))

	checkAllReleasable(t, c, sb, false)

	// examine contents of exported tars (requires containerd)
	cdAddress := sb.ContainerdAddress()
	if cdAddress == "" {
		t.Skip("rest of test requires containerd worker")
	}

	// TODO: make public pull helper function so this can be checked for standalone as well

	client, err := newContainerd(cdAddress)
	require.NoError(t, err)
	defer client.Close()

	ctx := namespaces.WithNamespace(sb.Context(), "buildkit")

	// check image in containerd
	_, err = client.ImageService().Get(ctx, target)
	require.NoError(t, err)

	// deleting image should release all content
	err = client.ImageService().Delete(ctx, target, images.SynchronousDelete())
	require.NoError(t, err)

	checkAllReleasable(t, c, sb, true)

	img, err := client.Pull(ctx, target)
	require.NoError(t, err)

	desc, err := img.Config(ctx)
	require.NoError(t, err)

	dt, err = content.ReadBlob(ctx, img.ContentStore(), desc)
	require.NoError(t, err)

	var ociimg ocispecs.Image
	err = json.Unmarshal(dt, &ociimg)
	require.NoError(t, err)

	require.NotEqual(t, "", ociimg.OS)
	require.NotEqual(t, "", ociimg.Architecture)
	require.NotEqual(t, "", ociimg.Config.WorkingDir)
	require.Equal(t, "layers", ociimg.RootFS.Type)
	require.Equal(t, 3, len(ociimg.RootFS.DiffIDs))
	require.NotNil(t, ociimg.Created)
	require.Less(t, time.Since(*ociimg.Created), 2*time.Minute)
	require.Condition(t, func() bool {
		for _, env := range ociimg.Config.Env {
			if strings.HasPrefix(env, "PATH=") {
				return true
			}
		}
		return false
	})

	require.Equal(t, 3, len(ociimg.History))
	require.Contains(t, ociimg.History[0].CreatedBy, "foo/sub/bar")
	require.Contains(t, ociimg.History[1].CreatedBy, "true")
	require.Contains(t, ociimg.History[2].CreatedBy, "foo/sub/baz")
	require.False(t, ociimg.History[0].EmptyLayer)
	require.False(t, ociimg.History[1].EmptyLayer)
	require.False(t, ociimg.History[2].EmptyLayer)

	dt, err = content.ReadBlob(ctx, img.ContentStore(), img.Target())
	require.NoError(t, err)

	mfst := struct {
		MediaType string `json:"mediaType,omitempty"`
		ocispecs.Manifest
	}{}

	err = json.Unmarshal(dt, &mfst)
	require.NoError(t, err)

	require.Equal(t, ocispecs.MediaTypeImageManifest, mfst.MediaType)
	require.Equal(t, 3, len(mfst.Layers))
	require.Equal(t, ocispecs.MediaTypeImageLayerGzip, mfst.Layers[0].MediaType)
	require.Equal(t, ocispecs.MediaTypeImageLayerGzip, mfst.Layers[1].MediaType)

	dt, err = content.ReadBlob(ctx, img.ContentStore(), ocispecs.Descriptor{Digest: mfst.Layers[0].Digest})
	require.NoError(t, err)

	m, err := testutil.ReadTarToMap(dt, true)
	require.NoError(t, err)

	item, ok := m["foo/"]
	require.True(t, ok)
	require.Equal(t, tar.TypeDir, int32(item.Header.Typeflag))
	require.Equal(t, 0741, int(item.Header.Mode&0777))

	item, ok = m["foo/sub/"]
	require.True(t, ok)
	require.Equal(t, tar.TypeDir, int32(item.Header.Typeflag))

	item, ok = m["foo/sub/bar"]
	require.True(t, ok)
	require.Equal(t, tar.TypeReg, int32(item.Header.Typeflag))
	require.Equal(t, []byte("first"), item.Data)

	_, ok = m["foo/sub/baz"]
	require.False(t, ok)

	dt, err = content.ReadBlob(ctx, img.ContentStore(), ocispecs.Descriptor{Digest: mfst.Layers[2].Digest})
	require.NoError(t, err)

	m, err = testutil.ReadTarToMap(dt, true)
	require.NoError(t, err)

	item, ok = m["foo/sub/baz"]
	require.True(t, ok)
	require.Equal(t, tar.TypeReg, int32(item.Header.Typeflag))
	require.Equal(t, []byte("second"), item.Data)

	item, ok = m["foo/"]
	require.True(t, ok)
	require.Equal(t, tar.TypeDir, int32(item.Header.Typeflag))
	require.Equal(t, 0741, int(item.Header.Mode&0777))

	item, ok = m["foo/sub/"]
	require.True(t, ok)
	require.Equal(t, tar.TypeDir, int32(item.Header.Typeflag))

	_, ok = m["foo/sub/bar"]
	require.False(t, ok)
}

// #877
func testExportBusyboxLocal(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	def, err := llb.Image("busybox").Marshal(sb.Context())
	require.NoError(t, err)

	destDir := t.TempDir()

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		FrontendAttrs: map[string]string{
			"attest:provenance": "",
		},
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			},
		},
	}, nil)
	require.NoError(t, err)

	fi, err := os.Stat(filepath.Join(destDir, "bin/busybox"))
	require.NoError(t, err)

	fi2, err := os.Stat(filepath.Join(destDir, "bin/vi"))
	require.NoError(t, err)

	require.True(t, os.SameFile(fi, fi2))
}

func testExportedImageLabels(t *testing.T, sb integration.Sandbox) {
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	cdAddress := sb.ContainerdAddress()
	if cdAddress == "" {
		t.Skip("only supported with containerd")
	}

	ctx := sb.Context()

	imgName := integration.UnixOrWindows("busybox", "nanoserver")
	prefix := integration.UnixOrWindows(
		"",
		"cmd /C ", // TODO(profnandaa): currently needs the shell prefix, to be fixed
	)
	def, err := llb.Image(imgName).
		Run(llb.Shlexf(fmt.Sprintf("%secho foo > /foo", prefix))).
		Marshal(ctx)
	require.NoError(t, err)

	target := "docker.io/buildkit/build/exporter:labels"

	_, err = c.Solve(ctx, def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name": target,
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	client, err := newContainerd(cdAddress)
	require.NoError(t, err)
	defer client.Close()

	ctx = namespaces.WithNamespace(ctx, "buildkit")

	img, err := client.GetImage(ctx, target)
	require.NoError(t, err)

	store := client.ContentStore()

	info, err := store.Info(ctx, img.Target().Digest)
	require.NoError(t, err)

	dt, err := content.ReadBlob(ctx, store, img.Target())
	require.NoError(t, err)

	var mfst ocispecs.Manifest
	err = json.Unmarshal(dt, &mfst)
	require.NoError(t, err)

	require.Equal(t, 2, len(mfst.Layers))

	hasLabel := func(dgst digest.Digest) bool {
		for k, v := range info.Labels {
			if strings.HasPrefix(k, "containerd.io/gc.ref.content.") && v == dgst.String() {
				return true
			}
		}
		return false
	}

	// check that labels are set on all layers and config
	for _, l := range mfst.Layers {
		require.True(t, hasLabel(l.Digest))
	}
	require.True(t, hasLabel(mfst.Config.Digest))

	err = c.Prune(sb.Context(), nil, PruneAll)
	require.NoError(t, err)

	// layer should not be deleted
	_, err = store.Info(ctx, mfst.Layers[1].Digest)
	require.NoError(t, err)

	err = client.ImageService().Delete(ctx, target, images.SynchronousDelete())
	require.NoError(t, err)

	// layers should be deleted
	_, err = store.Info(ctx, mfst.Layers[1].Digest)
	require.Error(t, err)
	require.True(t, errors.Is(err, cerrdefs.ErrNotFound))

	// config should be deleted
	_, err = store.Info(ctx, mfst.Config.Digest)
	require.Error(t, err)
	require.True(t, errors.Is(err, cerrdefs.ErrNotFound))

	// buildkit contentstore still has the layer because it is multi-ns
	bkstore := proxy.NewContentStore(c.ContentClient())

	// layer should be deleted as not kept by history
	_, err = bkstore.Info(ctx, mfst.Layers[1].Digest)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found")

	// config should still be there
	_, err = bkstore.Info(ctx, img.Metadata().Target.Digest)
	require.NoError(t, err)

	_, err = bkstore.Info(ctx, mfst.Config.Digest)
	require.NoError(t, err)

	cl, err := c.ControlClient().ListenBuildHistory(sb.Context(), &controlapi.BuildHistoryRequest{
		EarlyExit: true,
	})
	require.NoError(t, err)

	for {
		resp, err := cl.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		_, err = c.ControlClient().UpdateBuildHistory(sb.Context(), &controlapi.UpdateBuildHistoryRequest{
			Ref:    resp.Record.Ref,
			Delete: true,
		})
		require.NoError(t, err)
	}

	// now everything should be deleted
	_, err = bkstore.Info(ctx, img.Metadata().Target.Digest)
	require.Error(t, err)

	_, err = bkstore.Info(ctx, mfst.Config.Digest)
	require.Error(t, err)
}

func testImageResolveConfigDefaultLocalFallback(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureImageExporter)

	cdAddress := sb.ContainerdAddress()
	if cdAddress == "" {
		t.Skip("test requires containerd worker")
	}

	ctx := sb.Context()
	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	cdClient, err := newContainerd(cdAddress)
	require.NoError(t, err)
	defer cdClient.Close()

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	target := registry + "/buildkit/default-local-fallback:" + identity.NewID()
	st := integration.UnixOrWindows(
		llb.Scratch(),
		llb.Image("nanoserver:latest"),
	)
	def, err := st.File(llb.Mkfile("/fallback", 0600, []byte("local"))).Marshal(ctx)
	require.NoError(t, err)

	_, err = c.Solve(ctx, def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name":  target,
					"store": "true",
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	imageService := cdClient.ImageService()
	imageStoreCtx := namespaces.WithNamespace(ctx, "buildkit")
	img, err := imageService.Get(imageStoreCtx, target)
	require.NoError(t, err)

	_, err = c.Build(ctx, SolveOpt{}, "buildkit_test", func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		ref, dgst, config, err := c.ResolveImageConfig(ctx, target, sourceresolver.Opt{
			ImageOpt: &sourceresolver.ResolveImageOpt{
				ResolveMode: pb.AttrImageResolveModeDefault,
			},
		})
		if err != nil {
			return nil, err
		}
		require.Equal(t, target, ref)
		require.Equal(t, img.Target.Digest, dgst)
		require.NotEmpty(t, config)

		var ociimg ocispecs.Image
		require.NoError(t, json.Unmarshal(config, &ociimg))
		require.NotEmpty(t, ociimg.OS)
		require.NotEmpty(t, ociimg.Architecture)

		return gateway.NewResult(), nil
	}, nil)
	require.NoError(t, err)

	checkAllReleasable(t, c, sb, true)
}

func testInvalidExporter(t *testing.T, sb integration.Sandbox) {
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	imgName := integration.UnixOrWindows("busybox:latest", "nanoserver:latest")
	def, err := llb.Image(imgName).Marshal(sb.Context())
	require.NoError(t, err)

	destDir := t.TempDir()

	target := "example.com/buildkit/testoci:latest"
	attrs := map[string]string{
		"name": target,
	}
	for _, exp := range []string{ExporterOCI, ExporterDocker} {
		_, err = c.Solve(sb.Context(), def, SolveOpt{
			Exports: []ExportEntry{
				{
					Type:  exp,
					Attrs: attrs,
				},
			},
		}, nil)
		// output file writer is required
		require.Error(t, err)
		_, err = c.Solve(sb.Context(), def, SolveOpt{
			Exports: []ExportEntry{
				{
					Type:      exp,
					Attrs:     attrs,
					OutputDir: destDir,
				},
			},
		}, nil)
		// output directory is not supported
		require.Error(t, err)
	}

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:  ExporterLocal,
				Attrs: attrs,
			},
		},
	}, nil)
	// output directory is required
	require.Error(t, err)

	f, err := os.Create(filepath.Join(destDir, "a"))
	require.NoError(t, err)
	defer f.Close()
	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:   ExporterLocal,
				Attrs:  attrs,
				Output: fixedWriteCloser(f),
			},
		},
	}, nil)
	// output file writer is not supported
	require.Error(t, err)

	checkAllReleasable(t, c, sb, true)
}

func testLazyImagePush(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureDirectPush)
	cdAddress := sb.ContainerdAddress()
	if cdAddress == "" {
		t.Skip("test requires containerd worker")
	}

	client, err := newContainerd(cdAddress)
	require.NoError(t, err)
	defer client.Close()

	ctx := namespaces.WithNamespace(sb.Context(), "buildkit")

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	// push the busybox image to the mutable registry
	sourceImage := integration.UnixOrWindows("busybox:latest", "nanoserver:latest")
	def, err := llb.Image(sourceImage).Marshal(sb.Context())
	require.NoError(t, err)

	targetNoTag := registry + "/buildkit/testlazyimage:"
	target := targetNoTag + "latest"
	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name":                                   target,
					"push":                                   "true",
					"store":                                  "true",
					"unsafe-internal-store-allow-incomplete": "true",
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	imageService := client.ImageService()
	contentStore := client.ContentStore()

	img, err := imageService.Get(ctx, target)
	require.NoError(t, err)

	manifest, err := images.Manifest(ctx, contentStore, img.Target, nil)
	require.NoError(t, err)

	for _, layer := range manifest.Layers {
		_, err = contentStore.Info(ctx, layer.Digest)
		require.NoError(t, err)
	}

	// clear all local state out
	err = imageService.Delete(ctx, img.Name, images.SynchronousDelete())
	require.NoError(t, err)
	checkAllReleasable(t, c, sb, true)

	// retag the image we just pushed with no actual changes, which
	// should not result in the image getting un-lazied
	def, err = llb.Image(target).Marshal(sb.Context())
	require.NoError(t, err)

	target2 := targetNoTag + "newtag"
	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name":                                   target2,
					"push":                                   "true",
					"store":                                  "true",
					"unsafe-internal-store-allow-incomplete": "true",
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	img, err = imageService.Get(ctx, target2)
	require.NoError(t, err)

	manifest, err = images.Manifest(ctx, contentStore, img.Target, nil)
	require.NoError(t, err)

	for _, layer := range manifest.Layers {
		_, err = contentStore.Info(ctx, layer.Digest)
		require.ErrorIs(t, err, cerrdefs.ErrNotFound, "unexpected error %v", err)
	}

	// clear all local state out again
	err = imageService.Delete(ctx, img.Name, images.SynchronousDelete())
	require.NoError(t, err)
	checkAllReleasable(t, c, sb, true)

	// try a cross-repo push to same registry, which should still result in the
	// image remaining lazy
	target3 := registry + "/buildkit/testlazycrossrepo:latest"
	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name":                                   target3,
					"push":                                   "true",
					"store":                                  "true",
					"unsafe-internal-store-allow-incomplete": "true",
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	img, err = imageService.Get(ctx, target3)
	require.NoError(t, err)

	manifest, err = images.Manifest(ctx, contentStore, img.Target, nil)
	require.NoError(t, err)

	for _, layer := range manifest.Layers {
		_, err = contentStore.Info(ctx, layer.Digest)
		require.ErrorIs(t, err, cerrdefs.ErrNotFound, "unexpected error %v", err)
	}

	// check that a subsequent build can use the previously lazy image in an exec
	cmdStr := integration.UnixOrWindows("true", "cmd /C exit 0")
	def, err = llb.Image(target2).Run(llb.Args([]string{cmdStr})).Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.NoError(t, err)
}

func testOCIExporter(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureOCIExporter)
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	busybox := llb.Image("busybox:latest")
	st := llb.Scratch()

	run := func(cmd string) {
		st = busybox.Run(llb.Shlex(cmd), llb.Dir("/wd")).AddMount("/wd", st)
	}

	run(`sh -c "echo -n first > foo"`)
	run(`sh -c "echo -n second > bar"`)

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	target := "example.com/buildkit/testoci:latest"
	for _, tc := range []struct {
		name              string
		exporter          string
		attrs             map[string]string
		indexMediaType    string
		manifestMediaType string
		configMediaType   string
		layerMediaType    string
	}{
		{
			name:              ExporterOCI,
			exporter:          ExporterOCI,
			attrs:             map[string]string{},
			indexMediaType:    ocispecs.MediaTypeImageIndex,
			manifestMediaType: ocispecs.MediaTypeImageManifest,
			configMediaType:   ocispecs.MediaTypeImageConfig,
			layerMediaType:    ocispecs.MediaTypeImageLayerGzip,
		},
		{
			name:     ExporterDocker,
			exporter: ExporterDocker,
			attrs: map[string]string{
				"name": target,
			},
			indexMediaType:    ocispecs.MediaTypeImageIndex,
			manifestMediaType: ocispecs.MediaTypeImageManifest,
			configMediaType:   ocispecs.MediaTypeImageConfig,
			layerMediaType:    ocispecs.MediaTypeImageLayerGzip,
		},
		{
			name:     "docker-oci-mediatypes-false",
			exporter: ExporterDocker,
			attrs: map[string]string{
				"name":           target,
				"oci-mediatypes": "false",
			},
			indexMediaType:    ocispecs.MediaTypeImageIndex,
			manifestMediaType: images.MediaTypeDockerSchema2Manifest,
			configMediaType:   images.MediaTypeDockerSchema2Config,
			layerMediaType:    images.MediaTypeDockerSchema2LayerGzip,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			destDir := t.TempDir()

			out := filepath.Join(destDir, "out.tar")
			outW, err := os.Create(out)
			require.NoError(t, err)
			_, err = c.Solve(sb.Context(), def, SolveOpt{
				Exports: []ExportEntry{
					{
						Type:   tc.exporter,
						Attrs:  tc.attrs,
						Output: fixedWriteCloser(outW),
					},
				},
			}, nil)
			require.NoError(t, err)

			dt, err := os.ReadFile(out)
			require.NoError(t, err)

			m, err := testutil.ReadTarToMap(dt, false)
			require.NoError(t, err)

			_, ok := m["oci-layout"]
			require.True(t, ok)

			var index ocispecs.Index
			err = json.Unmarshal(m[ocispecs.ImageIndexFile].Data, &index)
			require.NoError(t, err)
			require.Equal(t, 2, index.SchemaVersion)
			require.Equal(t, tc.indexMediaType, index.MediaType)
			require.Equal(t, 1, len(index.Manifests))
			require.Equal(t, tc.manifestMediaType, index.Manifests[0].MediaType)

			var mfst ocispecs.Manifest
			err = json.Unmarshal(m[ocispecs.ImageBlobsDir+"/sha256/"+index.Manifests[0].Digest.Hex()].Data, &mfst)
			require.NoError(t, err)
			require.Equal(t, tc.manifestMediaType, mfst.MediaType)
			require.Equal(t, tc.configMediaType, mfst.Config.MediaType)
			require.Equal(t, 2, len(mfst.Layers))
			for _, layer := range mfst.Layers {
				require.Equal(t, tc.layerMediaType, layer.MediaType)
			}

			var ociimg ocispecs.Image
			err = json.Unmarshal(m[ocispecs.ImageBlobsDir+"/sha256/"+mfst.Config.Digest.Hex()].Data, &ociimg)
			require.NoError(t, err)
			require.Equal(t, "layers", ociimg.RootFS.Type)
			require.Equal(t, 2, len(ociimg.RootFS.DiffIDs))

			_, ok = m[ocispecs.ImageBlobsDir+"/sha256/"+mfst.Layers[0].Digest.Hex()]
			require.True(t, ok)
			_, ok = m[ocispecs.ImageBlobsDir+"/sha256/"+mfst.Layers[1].Digest.Hex()]
			require.True(t, ok)

			if tc.exporter != ExporterDocker {
				return
			}

			var dockerMfst []struct {
				Config   string
				RepoTags []string
				Layers   []string
			}
			err = json.Unmarshal(m["manifest.json"].Data, &dockerMfst)
			require.NoError(t, err)
			require.Equal(t, 1, len(dockerMfst))

			_, ok = m[dockerMfst[0].Config]
			require.True(t, ok)
			require.Equal(t, 2, len(dockerMfst[0].Layers))
			require.Equal(t, 1, len(dockerMfst[0].RepoTags))
			require.Equal(t, target, dockerMfst[0].RepoTags[0])

			for _, l := range dockerMfst[0].Layers {
				_, ok := m[l]
				require.True(t, ok)
			}
		})
	}

	checkAllReleasable(t, c, sb, true)
}

func testOCIExporterContentStore(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureOCIExporter)
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	busybox := llb.Image("busybox:latest")
	st := llb.Scratch()

	run := func(cmd string) {
		st = busybox.Run(llb.Shlex(cmd), llb.Dir("/wd")).AddMount("/wd", st)
	}

	run(`sh -c "echo -n first > foo"`)
	run(`sh -c "echo -n second > bar"`)

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	for _, exp := range []string{ExporterOCI, ExporterDocker} {
		destDir := t.TempDir()
		target := "example.com/buildkit/testoci:latest"

		outTar := filepath.Join(destDir, "out.tar")
		outW, err := os.Create(outTar)
		require.NoError(t, err)
		attrs := map[string]string{}
		if exp == ExporterDocker {
			attrs["name"] = target
		}
		_, err = c.Solve(sb.Context(), def, SolveOpt{
			Exports: []ExportEntry{
				{
					Type:   exp,
					Attrs:  attrs,
					Output: fixedWriteCloser(outW),
				},
			},
		}, nil)
		require.NoError(t, err)

		dt, err := os.ReadFile(outTar)
		require.NoError(t, err)
		m, err := testutil.ReadTarToMap(dt, false)
		require.NoError(t, err)

		checkStore := func(dir string) {
			err = filepath.Walk(dir, func(filename string, fi os.FileInfo, err error) error {
				filename = strings.TrimPrefix(filename, dir)
				filename = strings.Trim(filename, "/")
				if filename == "" || filename == "ingest" {
					return nil
				}

				if fi.IsDir() {
					require.Contains(t, m, filename+"/")
				} else {
					require.Contains(t, m, filename)
					if filename == ocispecs.ImageIndexFile {
						// this file has a timestamp in it, so we can't compare
						return nil
					}
					f, err := os.Open(path.Join(dir, filename))
					require.NoError(t, err)
					data, err := io.ReadAll(f)
					require.NoError(t, err)
					require.Equal(t, m[filename].Data, data)
				}
				return nil
			})
			require.NoError(t, err)
		}

		outDir := filepath.Join(destDir, "out.d")
		attrs = map[string]string{
			"tar": "false",
		}
		if exp == ExporterDocker {
			attrs["name"] = target
		}
		_, err = c.Solve(sb.Context(), def, SolveOpt{
			Exports: []ExportEntry{
				{
					Type:      exp,
					Attrs:     attrs,
					OutputDir: outDir,
				},
			},
		}, nil)
		require.NoError(t, err)
		checkStore(outDir)

		outStoreDir := filepath.Join(destDir, "store.d")
		store, err := local.NewStore(outStoreDir)
		require.NoError(t, err)
		attrs = map[string]string{
			"tar": "false",
		}
		if exp == ExporterDocker {
			attrs["name"] = target
		}
		_, err = c.Solve(sb.Context(), def, SolveOpt{
			Exports: []ExportEntry{
				{
					Type:        exp,
					Attrs:       attrs,
					OutputStore: store,
				},
			},
		}, nil)
		require.NoError(t, err)
		checkStore(outDir)
	}

	checkAllReleasable(t, c, sb, true)
}

func testPullWithDigestCheck(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureDirectPush)
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	st := llb.Scratch().File(llb.Mkfile("foo", 0600, []byte("data1")))

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	name1 := registry + "/foo/bar:v1.0.0"

	resp, err := c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type: "image",
				Attrs: map[string]string{
					"name": name1,
					"push": "true",
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	dgst1Str := resp.ExporterResponse[exptypes.ExporterImageDigestKey]
	dgst1, err := digest.Parse(dgst1Str)
	require.NoError(t, err)

	st = llb.Scratch().File(llb.Mkfile("foo", 0600, []byte("data2")))

	def, err = st.Marshal(sb.Context())
	require.NoError(t, err)

	name2 := registry + "/foo/bar:v2.0.0"

	resp, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type: "image",
				Attrs: map[string]string{
					"name": name2,
					"push": "true",
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	dgst2str := resp.ExporterResponse[exptypes.ExporterImageDigestKey]
	dgst2, err := digest.Parse(dgst2str)
	require.NoError(t, err)

	require.NotEqual(t, dgst1, dgst2)

	// if digest is set in ref then pull happens only by the digest
	st = llb.Image(name2 + "@" + dgst1.String())
	def, err = st.Marshal(sb.Context())
	require.NoError(t, err)

	destDir := t.TempDir()

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "foo"))
	require.NoError(t, err)
	require.Equal(t, "data1", string(dt))

	// if digest is set by checksum then pull happens by tag
	st = llb.Image(name2, llb.WithImageChecksum(dgst2))
	def, err = st.Marshal(sb.Context())
	require.NoError(t, err)
	destDir = t.TempDir()
	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err = os.ReadFile(filepath.Join(destDir, "foo"))
	require.NoError(t, err)
	require.Equal(t, "data2", string(dt))

	// if checksum doesn't match then pull fails
	st = llb.Image(name2, llb.WithImageChecksum(dgst1))
	def, err = st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), fmt.Sprintf("image digest %s for %s does not match expected checksum %s", dgst2, name2, dgst1))
}

// testPullZstdImage verifies pulling and re-exporting a Zstd-compressed image.
// It pushes a Zstd-compressed image to a registry, pulls it back, and validates
// the layer media types and Zstd magic bytes for both OCI and Docker schema 2.
func testPullZstdImage(t *testing.T, sb integration.Sandbox) {
	// Skipped on Windows:
	// 1. AddMount() fails because Windows snapshots allow only a single mount per layer.
	// 2. Zstd-compressed push/export fails because windowsLcowDiff lacks the Compare
	//    method needed to generate compressed layer diffs.
	integration.SkipOnPlatform(t, "windows", "Windows container support incomplete: AddMount() fails with 'number of mounts should always be 1 for Windows layers', Zstd export fails with 'windowsLcowDiff does not implement Compare method'")
	workers.CheckFeatureCompat(t, sb, workers.FeatureDirectPush)
	for _, ociMediaTypes := range []bool{true, false} {
		t.Run(t.Name()+fmt.Sprintf("/ociMediaTypes=%t", ociMediaTypes), func(t *testing.T) {
			c, err := New(sb.Context(), sb.Address())
			require.NoError(t, err)
			defer c.Close()

			busybox := llb.Image("busybox:latest")
			cmd := `sh -e -c "echo -n zstd > data"`

			st := llb.Scratch()
			st = busybox.Run(llb.Shlex(cmd), llb.Dir("/wd")).AddMount("/wd", st)

			def, err := st.Marshal(sb.Context())
			require.NoError(t, err)

			registry, err := sb.NewRegistry()
			if errors.Is(err, integration.ErrRequirements) {
				t.Skip(err.Error())
			}
			require.NoError(t, err)

			target := registry + "/buildkit/build/exporter:zstd"

			_, err = c.Solve(sb.Context(), def, SolveOpt{
				Exports: []ExportEntry{
					{
						Type: ExporterImage,
						Attrs: map[string]string{
							"name":           target,
							"push":           "true",
							"compression":    "zstd",
							"oci-mediatypes": strconv.FormatBool(ociMediaTypes),
						},
					},
				},
			}, nil)
			require.NoError(t, err)

			ensurePruneAll(t, c, sb)

			st = llb.Image(target).File(llb.Copy(llb.Image(target), "/data", "/zdata"))
			def, err = st.Marshal(sb.Context())
			require.NoError(t, err)

			destDir := t.TempDir()

			out := filepath.Join(destDir, "out.tar")
			outW, err := os.Create(out)
			require.NoError(t, err)

			_, err = c.Solve(sb.Context(), def, SolveOpt{
				Exports: []ExportEntry{
					{
						Type:   ExporterOCI,
						Output: fixedWriteCloser(outW),
						Attrs: map[string]string{
							"oci-mediatypes": strconv.FormatBool(ociMediaTypes),
						},
					},
				},
			}, nil)
			require.NoError(t, err)

			dt, err := os.ReadFile(out)
			require.NoError(t, err)

			m, err := testutil.ReadTarToMap(dt, false)
			require.NoError(t, err)

			var index ocispecs.Index
			err = json.Unmarshal(m[ocispecs.ImageIndexFile].Data, &index)
			require.NoError(t, err)

			var mfst ocispecs.Manifest
			err = json.Unmarshal(m[ocispecs.ImageBlobsDir+"/sha256/"+index.Manifests[0].Digest.Hex()].Data, &mfst)
			require.NoError(t, err)

			firstLayer := mfst.Layers[0]
			if ociMediaTypes {
				require.Equal(t, ocispecs.MediaTypeImageLayerZstd, firstLayer.MediaType)
			} else {
				require.Equal(t, images.MediaTypeDockerSchema2LayerZstd, firstLayer.MediaType)
			}

			zstdLayerDigest := firstLayer.Digest.Hex()
			require.Equal(t, []byte{0x28, 0xb5, 0x2f, 0xfd}, m[ocispecs.ImageBlobsDir+"/sha256/"+zstdLayerDigest].Data[:4])
		})
	}
}

func testPushByDigest(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureDirectPush)
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	st := llb.Scratch().File(llb.Mkfile("foo", 0600, []byte("data")))

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	name := registry + "/foo/bar"

	resp, err := c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type: "image",
				Attrs: map[string]string{
					"name":           name,
					"push":           "true",
					"push-by-digest": "true",
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	_, _, err = contentutil.ProviderFromRef(name + ":latest")
	require.Error(t, err)

	desc, _, err := contentutil.ProviderFromRef(name + "@" + resp.ExporterResponse[exptypes.ExporterImageDigestKey])
	require.NoError(t, err)

	require.Equal(t, resp.ExporterResponse[exptypes.ExporterImageDigestKey], desc.Digest.String())
	require.Equal(t, ocispecs.MediaTypeImageManifest, desc.MediaType)
	require.Greater(t, desc.Size, int64(0))
}

func testPushProgressSameVertex(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureDirectPush)
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	st := llb.Scratch().File(llb.Mkfile("foo", 0600, []byte("data")))
	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	imageName := registry + "/foo/bar:latest"

	var vertexes []*Vertex
	var statuses []*VertexStatus
	ch := make(chan *SolveStatus)
	eg, ctx := errgroup.WithContext(sb.Context())
	eg.Go(func() error {
		for {
			select {
			case <-ctx.Done():
				return context.Cause(ctx)
			case ss, ok := <-ch:
				if !ok {
					return nil
				}
				vertexes = append(vertexes, ss.Vertexes...)
				statuses = append(statuses, ss.Statuses...)
			}
		}
	})
	eg.Go(func() error {
		_, err := c.Solve(ctx, def, SolveOpt{
			Exports: []ExportEntry{
				{
					Type: "image",
					Attrs: map[string]string{
						"name": imageName,
						"push": "true",
					},
				},
			},
		}, ch)
		return err
	})
	require.NoError(t, eg.Wait())

	exportVertexes := map[digest.Digest]struct{}{}
	pushVertexes := map[digest.Digest]struct{}{}
	pushManifestPrefix := "pushing manifest for " + imageName + "@"

	for _, v := range vertexes {
		if v.Name == "exporting to image" {
			exportVertexes[v.Digest] = struct{}{}
		}
	}

	for _, st := range statuses {
		switch {
		case st.ID == "pushing layers":
			pushVertexes[st.Vertex] = struct{}{}
		case strings.HasPrefix(st.ID, pushManifestPrefix):
			pushVertexes[st.Vertex] = struct{}{}
		}
	}

	require.Len(t, exportVertexes, 1, "expected exactly one export vertex in status stream")
	require.NotEmpty(t, pushVertexes, "expected at least one push status in status stream")

	var exportVertex digest.Digest
	for v := range exportVertexes {
		exportVertex = v
	}
	for v := range pushVertexes {
		require.Equal(t, exportVertex, v, "push statuses should use the same vertex as exporting to image")
	}

	hasPushManifest := false
	for _, st := range statuses {
		if strings.HasPrefix(st.ID, pushManifestPrefix) {
			hasPushManifest = true
			require.Equal(t, exportVertex, st.Vertex, "pushing manifest should use the same vertex as exporting to image")
		}
	}
	require.True(t, hasPushManifest, "expected pushing manifest status in status stream")
}

func testStargzLazyPull(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)

	cdAddress := sb.ContainerdAddress()
	if cdAddress == "" || sb.Snapshotter() != "stargz" {
		t.Skip("test requires containerd worker with stargz snapshotter")
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

	// Prepare stargz image
	orgImage := "docker.io/library/alpine:latest"
	sgzImage := registry + "/stargz/alpine:" + identity.NewID()
	def, err := llb.Image(orgImage).Marshal(sb.Context())
	require.NoError(t, err)
	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name":              sgzImage,
					"push":              "true",
					"compression":       "estargz",
					"oci-mediatypes":    "true",
					"force-compression": "true",
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	// clear all local state out
	err = imageService.Delete(ctx, sgzImage, images.SynchronousDelete())
	require.NoError(t, err)
	checkAllReleasable(t, c, sb, true)

	// stargz layers should be lazy even for executing something on them
	def, err = llb.Image(sgzImage).
		Run(llb.Args([]string{"sh", "-c", "cat /dev/urandom | head -c 100 | sha256sum > unique"})).
		Marshal(sb.Context())
	require.NoError(t, err)
	target := registry + "/buildkit/testlazyimage:" + identity.NewID()
	resp, err := c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name":                                   target,
					"push":                                   "true",
					"oci-mediatypes":                         "true",
					"store":                                  "true",
					"unsafe-internal-store-allow-incomplete": "true",
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	dgst, ok := resp.ExporterResponse[exptypes.ExporterImageDigestKey]
	require.Equal(t, true, ok)

	unique, err := readFileInImage(sb.Context(), t, c, target+"@"+dgst, "/unique")
	require.NoError(t, err)

	img, err := imageService.Get(ctx, target)
	require.NoError(t, err)

	manifest, err := images.Manifest(ctx, contentStore, img.Target, nil)
	require.NoError(t, err)

	// Check if image layers are lazy.
	// The topmost(last) layer created by `Run` isn't lazy so we skip the check for the layer.
	var sgzLayers []ocispecs.Descriptor
	for _, layer := range manifest.Layers[:len(manifest.Layers)-1] {
		_, err = contentStore.Info(ctx, layer.Digest)
		require.ErrorIs(t, err, cerrdefs.ErrNotFound, "unexpected error %v", err)
		sgzLayers = append(sgzLayers, layer)
	}
	require.NotEqual(t, 0, len(sgzLayers), "no layer can be used for checking lazypull")

	// The topmost(last) layer created by `Run` shouldn't be lazy
	_, err = contentStore.Info(ctx, manifest.Layers[len(manifest.Layers)-1].Digest)
	require.NoError(t, err)

	// Run build again and check if cache is reused
	resp, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name":                                   target,
					"push":                                   "true",
					"store":                                  "true",
					"oci-mediatypes":                         "true",
					"unsafe-internal-store-allow-incomplete": "true",
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	dgst2, ok := resp.ExporterResponse[exptypes.ExporterImageDigestKey]
	require.Equal(t, true, ok)

	unique2, err := readFileInImage(sb.Context(), t, c, target+"@"+dgst2, "/unique")
	require.NoError(t, err)

	require.Equal(t, dgst, dgst2)
	require.Equal(t, unique, unique2)

	// clear all local state out
	err = imageService.Delete(ctx, img.Name, images.SynchronousDelete())
	require.NoError(t, err)
	checkAllReleasable(t, c, sb, true)

	// stargz layers should be exportable
	destDir := t.TempDir()
	out := filepath.Join(destDir, "out.tar")
	outW, err := os.Create(out)
	require.NoError(t, err)
	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:   ExporterOCI,
				Output: fixedWriteCloser(outW),
			},
		},
	}, nil)
	require.NoError(t, err)

	// Check if image layers are un-lazied
	for _, layer := range sgzLayers {
		_, err = contentStore.Info(ctx, layer.Digest)
		require.NoError(t, err)
	}

	ensurePruneAll(t, c, sb)
}
