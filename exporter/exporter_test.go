package exporter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/platforms"
	intoto "github.com/in-toto/in-toto-golang/in_toto"
	"github.com/moby/buildkit/cache/config"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	"github.com/moby/buildkit/frontend/dockerui"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	ptypes "github.com/moby/buildkit/solver/llbsolver/provenance/types"
	"github.com/moby/buildkit/util/compression"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/moby/buildkit/util/testutil/workers"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
	"github.com/tonistiigi/fsutil"
)

func init() {
	if workers.IsTestDockerd() {
		workers.InitDockerdWorker()
	} else {
		workers.InitOCIWorker()
		workers.InitContainerdWorker()
	}
}

func TestExporterIntegration(t *testing.T) {
	testIntegration(t,
		testGatewayExporter,
		testGatewayIsolatedRemotes,
		testExternalExporter,
		testExternalExporterMultiplatform,
		testGatewayExporterCompression,
	)
}

func testIntegration(t *testing.T, funcs ...func(t *testing.T, sb integration.Sandbox)) {
	// NOTE: to build the test exporter image, we need to mirror all the Dockerfile images
	mirroredImages := integration.OfficialImages("golang:1.25-alpine3.22")
	mirroredImages["tonistiigi/xx:1.6.1"] = "docker.io/tonistiigi/xx:1.6.1"
	mirrors := integration.WithMirroredImages(mirroredImages)

	tests := integration.TestFuncs(funcs...)
	integration.Run(t, tests, mirrors)
}

func testGatewayExporter(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureOCIExporter, workers.FeatureOCILayout)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	frontend := func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		st := llb.Scratch().
			File(llb.Mkfile("/foo.txt", 0644, []byte("foo"))).
			File(llb.Mkfile("/bar.txt", 0644, []byte("bar")))
		def, err := st.Marshal(sb.Context())
		if err != nil {
			return nil, err
		}
		return c.Solve(ctx, gateway.SolveRequest{
			Definition: def.ToPB(),
		})
	}

	var foundFiles []string
	var foundDescs []ocispecs.Descriptor
	export := func(ctx context.Context, c gateway.Client, handle gateway.ExportHandle, result *gateway.Result) error {
		entries, err := result.Ref.ReadDir(ctx, gateway.ReadDirRequest{Path: "/"})
		if err != nil {
			return err
		}
		for _, entry := range entries {
			foundFiles = append(foundFiles, entry.Path)
		}

		descs, err := result.Ref.GetRemote(ctx, config.RefConfig{})
		if err != nil {
			return err
		}
		err = checkDescriptors(ctx, handle.ContentStore(), descs)
		if err != nil {
			return err
		}
		foundDescs = descs

		return nil
	}

	_, err = c.BuildExport(sb.Context(), client.SolveOpt{}, "", frontend, export, nil)
	require.NoError(t, err)

	require.Equal(t, []string{"bar.txt", "foo.txt"}, foundFiles)
	require.Len(t, foundDescs, 2) // 2 layers
}

func testGatewayIsolatedRemotes(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureOCIExporter, workers.FeatureOCILayout)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	p := platforms.DefaultSpec()
	ps := platforms.Format(p)

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	exporter := registry + "/buildkit/exporter/sample-isolate:latest"
	err = buildTestExporter(sb.Context(), c, exporter)
	require.NoError(t, err)

	frontend := func(content string) gateway.BuildFunc {
		return func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
			st := llb.Scratch().File(llb.Mkfile("/file.txt", 0644, []byte(content)))
			def, err := st.Marshal(sb.Context())
			if err != nil {
				return nil, err
			}
			return c.Solve(ctx, gateway.SolveRequest{
				Definition: def.ToPB(),
			})
		}
	}

	destFile := filepath.Join(t.TempDir(), "output.txt")
	fileOutput := func(map[string]string) (io.WriteCloser, error) {
		return os.Create(destFile)
	}

	_, err = c.Build(sb.Context(), client.SolveOpt{
		Exports: []client.ExportEntry{
			{
				Type:   "gateway",
				Output: fileOutput,
				Attrs: map[string]string{
					"source": exporter,
				},
			},
		},
	}, "", frontend("foo"), nil)
	require.NoError(t, err)

	reportData, err := os.ReadFile(destFile)
	require.NoError(t, err)
	report := &report{}
	err = json.Unmarshal(reportData, report)
	require.NoError(t, err)
	require.NotEmpty(t, report.Refs)
	require.NotEmpty(t, report.Refs[ps].Layers)

	descsDt, err := json.Marshal(report.Refs[ps].Layers)
	require.NoError(t, err)

	_, err = c.Build(sb.Context(), client.SolveOpt{
		Exports: []client.ExportEntry{
			{
				Type: "gateway",
				Attrs: map[string]string{
					"source":      exporter,
					"fetch-descs": string(descsDt),
				},
			},
		},
	}, "", frontend("foo"), nil)
	// should succeed, because this is the same content, so it's accessible
	require.NoError(t, err)

	_, err = c.Build(sb.Context(), client.SolveOpt{
		Exports: []client.ExportEntry{
			{
				Type: "gateway",
				Attrs: map[string]string{
					"source":      exporter,
					"fetch-descs": string(descsDt),
				},
			},
		},
	}, "", frontend("bar"), nil)
	// should fail because the content store is isolated per build
	require.Error(t, err)
	require.ErrorContains(t, err, "failed to get reader for fetch-desc")
}

func testExternalExporter(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureOCIExporter, workers.FeatureOCILayout)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	exporter := registry + "/buildkit/exporter/sample:latest"
	err = buildTestExporter(sb.Context(), c, exporter)
	require.NoError(t, err)

	destFile := filepath.Join(t.TempDir(), "output.txt")
	fileOutput := func(map[string]string) (io.WriteCloser, error) {
		return os.Create(destFile)
	}

	destDir := filepath.Join(t.TempDir(), "outputd")

	frontend := func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		st := llb.Scratch().
			File(llb.Mkfile("/foo.txt", 0644, []byte("foo"))).
			File(llb.Mkfile("/bar.txt", 0644, []byte("bar")))
		def, err := st.Marshal(sb.Context())
		if err != nil {
			return nil, err
		}

		res, err := c.Solve(ctx, gateway.SolveRequest{
			Definition: def.ToPB(),
		})
		if err != nil {
			return nil, err
		}

		img := ocispecs.Image{
			Platform: platforms.DefaultSpec(),
			Config: ocispecs.ImageConfig{
				Labels: map[string]string{
					"testlabel": "testvalue",
				},
			},
		}
		config, err := json.Marshal(img)
		if err != nil {
			return nil, err
		}
		res.AddMeta(exptypes.ExporterImageConfigKey, config)

		return res, nil
	}

	_, err = c.Build(sb.Context(), client.SolveOpt{
		FrontendAttrs: map[string]string{
			"attest:provenance": "mode=max",
		},
		Exports: []client.ExportEntry{
			{
				Type:   "gateway",
				Output: fileOutput,
				Attrs: map[string]string{
					"opt1":   "value1",
					"source": exporter,
				},
			},
			{
				Type:      "gateway",
				OutputDir: destDir,
				Attrs: map[string]string{
					"opt1":   "value1",
					"source": exporter,
				},
			},
		},
	}, "", frontend, nil)
	require.NoError(t, err)

	destFileData, err := os.ReadFile(destFile)
	require.NoError(t, err)
	destDirData, err := os.ReadFile(destDir + "/report.json")
	require.NoError(t, err)

	fileReport := &report{}
	err = json.Unmarshal(destFileData, fileReport)
	require.NoError(t, err)
	require.Equal(t, "file", fileReport.Target)
	dirReport := &report{}
	err = json.Unmarshal(destDirData, dirReport)
	require.NoError(t, err)
	require.Equal(t, "directory", dirReport.Target)

	for _, report := range []*report{fileReport, dirReport} {
		require.Contains(t, report.Opts, "opt1")
		require.Equal(t, "value1", report.Opts["opt1"])

		require.NotEmpty(t, report.Platforms)
		require.NotEmpty(t, report.Refs)
		require.Equal(t, len(report.Platforms), len(report.Refs))

		for p, ref := range report.Refs {
			require.Contains(t, report.Platforms, p)

			var img ocispecs.Image
			err := json.Unmarshal(ref.Config, &img)
			require.NoError(t, err)
			require.Equal(t, "testvalue", img.Config.Labels["testlabel"])

			// files checked using ReadDir/ReadFile
			require.Equal(t, []string{"/bar.txt", "/foo.txt"}, ref.AllFiles)

			// layers read using content api
			require.Equal(t, 2, len(ref.Layers))
			require.Equal(t, []string{"foo.txt"}, ref.LayerFiles[ref.Layers[0].Digest])
			require.Equal(t, []string{"bar.txt"}, ref.LayerFiles[ref.Layers[1].Digest])

			// attestations created
			require.GreaterOrEqual(t, len(ref.Attestations), 1)
			for _, att := range ref.Attestations {
				require.Equal(t, intoto.StatementInTotoV01, att.Type)
				require.Equal(t, "report.json", att.Subject[0].Name)
				require.Equal(t, ptypes.BuildKitBuildType02, att.Predicate.(map[string]any)["buildType"])
			}
		}
	}
}

func testExternalExporterMultiplatform(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureOCIExporter, workers.FeatureOCILayout)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	exporter := registry + "/buildkit/exporter/sample-mp:latest"
	err = buildTestExporter(sb.Context(), c, exporter)
	require.NoError(t, err)

	destFile := filepath.Join(t.TempDir(), "output.txt")
	fileOutput := func(map[string]string) (io.WriteCloser, error) {
		return os.Create(destFile)
	}

	destDir := filepath.Join(t.TempDir(), "outputd")

	platformsToTest := []string{"linux/amd64", "linux/arm64"}

	frontend := func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		st := llb.Scratch().
			File(llb.Mkfile("/foo.txt", 0644, []byte("foo"))).
			File(llb.Mkfile("/bar.txt", 0644, []byte("bar")))
		def, err := st.Marshal(sb.Context())
		if err != nil {
			return nil, err
		}

		res, err := c.Solve(ctx, gateway.SolveRequest{
			Definition: def.ToPB(),
		})
		if err != nil {
			return nil, err
		}
		ref := res.Ref

		res = gateway.NewResult()
		expPlatforms := &exptypes.Platforms{
			Platforms: make([]exptypes.Platform, len(platformsToTest)),
		}
		for i, platform := range platformsToTest {
			platformSpec := platforms.MustParse(platform)
			expPlatforms.Platforms[i] = exptypes.Platform{ID: platform, Platform: platformSpec}
			img := ocispecs.Image{
				Platform: platformSpec,
				Config: ocispecs.ImageConfig{
					Labels: map[string]string{
						"testlabel": "testvalue",
					},
				},
			}
			config, err := json.Marshal(img)
			if err != nil {
				return nil, err
			}
			res.AddRef(platform, ref)
			res.AddMeta(fmt.Sprintf("%s/%s", exptypes.ExporterImageConfigKey, platform), config)
		}
		dt, err := json.Marshal(expPlatforms)
		if err != nil {
			return nil, err
		}
		res.AddMeta(exptypes.ExporterPlatformsKey, dt)

		return res, nil
	}

	_, err = c.Build(sb.Context(), client.SolveOpt{
		FrontendAttrs: map[string]string{
			"attest:provenance": "mode=max",
		},
		Exports: []client.ExportEntry{
			{
				Type:   "gateway",
				Output: fileOutput,
				Attrs: map[string]string{
					"opt1":   "value1",
					"source": exporter,
				},
			},
			{
				Type:      "gateway",
				OutputDir: destDir,
				Attrs: map[string]string{
					"opt1":   "value1",
					"source": exporter,
				},
			},
		},
	}, "", frontend, nil)
	require.NoError(t, err)

	destFileData, err := os.ReadFile(destFile)
	require.NoError(t, err)
	destDirData, err := os.ReadFile(destDir + "/report.json")
	require.NoError(t, err)

	fileReport := &report{}
	err = json.Unmarshal(destFileData, fileReport)
	require.NoError(t, err)
	require.Equal(t, "file", fileReport.Target)
	dirReport := &report{}
	err = json.Unmarshal(destDirData, dirReport)
	require.NoError(t, err)
	require.Equal(t, "directory", dirReport.Target)

	for _, report := range []*report{fileReport, dirReport} {
		require.Contains(t, report.Opts, "opt1")
		require.Equal(t, "value1", report.Opts["opt1"])

		require.Len(t, report.Platforms, 2)
		require.Len(t, report.Refs, 2)
		require.Contains(t, report.Platforms, "linux/amd64")
		require.Contains(t, report.Platforms, "linux/arm64")

		for p, ref := range report.Refs {
			require.Contains(t, report.Platforms, p)

			var img ocispecs.Image
			err := json.Unmarshal(ref.Config, &img)
			require.NoError(t, err)
			require.Equal(t, "testvalue", img.Config.Labels["testlabel"])

			// files checked using ReadDir/ReadFile
			require.Equal(t, []string{"/bar.txt", "/foo.txt"}, ref.AllFiles)

			// layers read using content api
			require.Equal(t, 2, len(ref.Layers))
			require.Equal(t, []string{"foo.txt"}, ref.LayerFiles[ref.Layers[0].Digest])
			require.Equal(t, []string{"bar.txt"}, ref.LayerFiles[ref.Layers[1].Digest])

			// attestations created
			require.GreaterOrEqual(t, len(ref.Attestations), 1)
			for _, att := range ref.Attestations {
				require.Equal(t, intoto.StatementInTotoV01, att.Type)
				require.Equal(t, "report.json", att.Subject[0].Name)
				require.Equal(t, ptypes.BuildKitBuildType02, att.Predicate.(map[string]any)["buildType"])
			}
		}
	}
}

func testGatewayExporterCompression(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureOCIExporter, workers.FeatureOCILayout)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	frontend := func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		st := llb.Scratch().File(llb.Mkfile("/foo.txt", 0644, []byte("foo")))
		def, err := st.Marshal(sb.Context())
		if err != nil {
			return nil, err
		}
		return c.Solve(ctx, gateway.SolveRequest{
			Definition: def.ToPB(),
		})
	}

	var uncompressedDescs, gzipDescs, zstdDescs []ocispecs.Descriptor
	export := func(ctx context.Context, c gateway.Client, handle gateway.ExportHandle, result *gateway.Result) error {
		store := handle.ContentStore()

		descs, err := result.Ref.GetRemote(ctx, config.RefConfig{
			Compression: compression.New(compression.Uncompressed).SetForce(true),
		})
		if err != nil {
			return err
		}
		if err := checkDescriptors(ctx, store, descs); err != nil {
			return err
		}
		uncompressedDescs = descs

		descs, err = result.Ref.GetRemote(ctx, config.RefConfig{
			Compression: compression.New(compression.Gzip).SetForce(true),
		})
		if err != nil {
			return err
		}
		if err := checkDescriptors(ctx, store, descs); err != nil {
			return err
		}
		gzipDescs = descs

		descs, err = result.Ref.GetRemote(ctx, config.RefConfig{
			Compression: compression.New(compression.Zstd).SetForce(true),
		})
		if err != nil {
			return err
		}
		if err := checkDescriptors(ctx, store, descs); err != nil {
			return err
		}
		zstdDescs = descs

		return nil
	}

	_, err = c.BuildExport(sb.Context(), client.SolveOpt{}, "", frontend, export, nil)
	require.NoError(t, err)

	require.Len(t, uncompressedDescs, 1)
	for _, desc := range uncompressedDescs {
		require.Equal(t, ocispecs.MediaTypeImageLayer, desc.MediaType)
	}

	_ = gzipDescs
	for _, desc := range gzipDescs {
		require.Equal(t, ocispecs.MediaTypeImageLayerGzip, desc.MediaType)
	}

	require.Len(t, zstdDescs, 1)
	for _, desc := range zstdDescs {
		require.Equal(t, ocispecs.MediaTypeImageLayerZstd, desc.MediaType)
	}
}

func buildTestExporter(ctx context.Context, c *client.Client, dest string) error {
	gatewayDir, err := fsutil.NewFS(integration.BuildkitSourcePath)
	if err != nil {
		return err
	}

	exporter := dest
	_, err = c.Solve(ctx, nil, client.SolveOpt{
		Frontend: "dockerfile.v0",
		FrontendAttrs: map[string]string{
			"filename": "exporter/gateway/test/Dockerfile",
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: gatewayDir,
			dockerui.DefaultLocalNameContext:    gatewayDir,
		},
		Exports: []client.ExportEntry{
			{
				Type: client.ExporterImage,
				Attrs: map[string]string{
					"name": exporter,
					"push": "true",
				},
			},
		},
	}, nil)
	return err
}

type report struct {
	Opts      map[string]string `json:"opts"`
	Target    string            `json:"target"`
	Platforms []string          `json:"platforms"`

	Refs map[string]*reportRef `json:"refs"`
}

type reportRef struct {
	Config json.RawMessage `json:"config"`

	AllFiles []string `json:"all_files"`

	Layers     []ocispecs.Descriptor      `json:"layers"`
	LayerFiles map[digest.Digest][]string `json:"layer_files"`

	Attestations []intoto.Statement `json:"attestations"`
}

func checkDescriptors(ctx context.Context, store content.Store, descs []ocispecs.Descriptor) error {
	for _, desc := range descs {
		r, err := store.ReaderAt(ctx, desc)
		if err != nil {
			return err
		}
		defer r.Close()
		sr := io.NewSectionReader(r, 0, r.Size())
		_, err = io.Copy(io.Discard, sr)
		if err != nil {
			return err
		}
	}
	return nil
}
