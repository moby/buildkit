package client

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	ctd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/platforms"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/iohelper"
	"github.com/moby/buildkit/util/testutil"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/moby/buildkit/util/testutil/workers"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
)

func testFrontendImageNaming(t *testing.T, sb integration.Sandbox) {
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	checkImageName := map[string]func(out, imageName string, exporterResponse map[string]string){
		ExporterOCI: func(out, imageName string, exporterResponse map[string]string) {
			// Nothing to check
		},
		ExporterDocker: func(out, imageName string, exporterResponse map[string]string) {
			require.Contains(t, exporterResponse, exptypes.ExporterImageNameKey)
			require.Equal(t, exporterResponse[exptypes.ExporterImageNameKey], "docker.io/library/"+imageName)

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
			require.Equal(t, 1, len(index.Manifests))

			var dockerMfst []struct {
				RepoTags []string
			}
			err = json.Unmarshal(m["manifest.json"].Data, &dockerMfst)
			require.NoError(t, err)
			require.Equal(t, 1, len(dockerMfst))
			require.Equal(t, 1, len(dockerMfst[0].RepoTags))
			require.Equal(t, imageName, dockerMfst[0].RepoTags[0])
		},
		ExporterImage: func(_, imageName string, exporterResponse map[string]string) {
			require.Contains(t, exporterResponse, exptypes.ExporterImageNameKey)
			require.Equal(t, exporterResponse[exptypes.ExporterImageNameKey], imageName)

			// check if we can pull (requires containerd)
			cdAddress := sb.ContainerdAddress()
			if cdAddress == "" {
				return
			}

			// TODO: make public pull helper function so this can be checked for standalone as well

			client, err := ctd.New(cdAddress)
			require.NoError(t, err)
			defer client.Close()

			ctx := namespaces.WithNamespace(sb.Context(), "buildkit")

			// check image in containerd
			_, err = client.ImageService().Get(ctx, imageName)
			require.NoError(t, err)

			// deleting image should release all content
			err = client.ImageService().Delete(ctx, imageName, images.SynchronousDelete())
			require.NoError(t, err)

			checkAllReleasable(t, c, sb, true)

			_, err = client.Pull(ctx, imageName)
			require.NoError(t, err)

			err = client.ImageService().Delete(ctx, imageName, images.SynchronousDelete())
			require.NoError(t, err)
		},
	}

	// A caller provided name takes precedence over one returned by the frontend. Iterate over both options.
	for _, winner := range []string{"frontend", "caller"} {
		// The double layer of `t.Run` here is required so
		// that the inner-most tests (with the actual
		// functionality) have definitely completed before the
		// sandbox and registry cleanups (defered above) are run.
		t.Run(winner, func(t *testing.T) {
			for _, exp := range []string{ExporterOCI, ExporterDocker, ExporterImage} {
				t.Run(exp, func(t *testing.T) {
					destDir := t.TempDir()

					so := SolveOpt{
						Exports: []ExportEntry{
							{
								Type:  exp,
								Attrs: map[string]string{},
							},
						},
					}

					out := filepath.Join(destDir, "out.tar")

					imageName := "image-" + exp + "-fe:latest"

					switch exp {
					case ExporterOCI:
						workers.CheckFeatureCompat(t, sb, workers.FeatureOCIExporter)
						t.Skip("oci exporter does not support named images")
					case ExporterDocker:
						workers.CheckFeatureCompat(t, sb, workers.FeatureOCIExporter)
						outW, err := os.Create(out)
						require.NoError(t, err)
						so.Exports[0].Output = fixedWriteCloser(outW)
					case ExporterImage:
						workers.CheckFeatureCompat(t, sb, workers.FeatureDirectPush)
						imageName = registry + "/" + imageName
						so.Exports[0].Attrs["push"] = "true"
					}

					feName := imageName
					switch winner {
					case "caller":
						feName = "loser:latest"
						so.Exports[0].Attrs["name"] = imageName
					case "frontend":
						so.Exports[0].Attrs["name"] = "*"
					}

					frontend := func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
						res := gateway.NewResult()
						res.AddMeta("image.name", []byte(feName))
						return res, nil
					}

					resp, err := c.Build(sb.Context(), so, "", frontend, nil)
					require.NoError(t, err)

					checkImageName[exp](out, imageName, resp.ExporterResponse)
				})
			}
		})
	}

	checkAllReleasable(t, c, sb, true)
}

func testFrontendLintSkipVerifyPlatforms(t *testing.T, sb integration.Sandbox) {
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	frontend := func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		dockerfile := `
FROM scratch
COPY foo /foo
    `
		st := llb.Scratch().File(
			llb.Mkfile("Dockerfile", 0600, []byte(dockerfile)).
				Mkfile("foo", 0600, []byte("data")))

		def, err := st.Marshal(sb.Context())
		if err != nil {
			return nil, err
		}

		return c.Solve(ctx, gateway.SolveRequest{
			FrontendInputs: map[string]*pb.Definition{
				"context":    def.ToPB(),
				"dockerfile": def.ToPB(),
			},
			FrontendOpt: map[string]string{
				"requestid":     "frontend.lint",
				"frontend.caps": "moby.buildkit.frontend.subrequests",
			},
		})
	}

	wc := newWarningsCapture()
	_, err = c.Build(sb.Context(), SolveOpt{
		FrontendAttrs: map[string]string{
			"platform": "linux/amd64,linux/arm64",
		},
	}, "", frontend, wc.status)
	require.NoError(t, err)
	warnings := wc.wait()

	for _, w := range warnings {
		t.Logf("warning: %s", string(w.Short))
	}
	require.Len(t, warnings, 0)
}

func testFrontendMetadataReturn(t *testing.T, sb integration.Sandbox) {
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	frontend := func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		res := gateway.NewResult()
		res.AddMeta("frontend.returned", []byte("true"))
		res.AddMeta("not-frontend.not-returned", []byte("false"))
		res.AddMeta("frontendnot.returned.either", []byte("false"))
		return res, nil
	}

	var exports []ExportEntry
	if workers.IsTestDockerdMoby(sb) {
		exports = []ExportEntry{{
			Type: "moby",
			Attrs: map[string]string{
				"name": "reg.dummy:5000/buildkit/test:latest",
			},
		}}
	} else {
		exports = []ExportEntry{{
			Type:   ExporterOCI,
			Attrs:  map[string]string{},
			Output: fixedWriteCloser(&iohelper.NopWriteCloser{Writer: io.Discard}),
		}}
	}

	res, err := c.Build(sb.Context(), SolveOpt{
		Exports: exports,
	}, "", frontend, nil)
	require.NoError(t, err)
	require.Contains(t, res.ExporterResponse, "frontend.returned")
	require.Equal(t, "true", res.ExporterResponse["frontend.returned"])
	require.NotContains(t, res.ExporterResponse, "not-frontend.not-returned")
	require.NotContains(t, res.ExporterResponse, "frontendnot.returned.either")
	checkAllReleasable(t, c, sb, true)
}

func testFrontendUseSolveResults(t *testing.T, sb integration.Sandbox) {
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	frontend := func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		st := llb.Scratch().File(
			llb.Mkfile("foo", 0600, []byte("data")),
		)

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

		ref, err := res.SingleRef()
		if err != nil {
			return nil, err
		}

		st2, err := ref.ToState()
		if err != nil {
			return nil, err
		}

		st = llb.Scratch().File(
			llb.Copy(st2, "foo", "foo2"),
		)

		def, err = st.Marshal(sb.Context())
		if err != nil {
			return nil, err
		}

		return c.Solve(ctx, gateway.SolveRequest{
			Definition: def.ToPB(),
		})
	}

	destDir := t.TempDir()

	_, err = c.Build(sb.Context(), SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			},
		},
	}, "", frontend, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "foo2"))
	require.NoError(t, err)
	require.Equal(t, dt, []byte("data"))
}

func testFrontendVerifyPlatforms(t *testing.T, sb integration.Sandbox) {
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	frontend := func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		st := llb.Scratch().File(
			llb.Mkfile("foo", 0600, []byte("data")),
		)

		def, err := st.Marshal(sb.Context())
		if err != nil {
			return nil, err
		}

		return c.Solve(ctx, gateway.SolveRequest{
			Definition: def.ToPB(),
		})
	}

	wc := newWarningsCapture()
	_, err = c.Build(sb.Context(), SolveOpt{
		FrontendAttrs: map[string]string{
			"platform": "linux/amd64,linux/arm64",
		},
	}, "", frontend, wc.status)
	require.NoError(t, err)
	warnings := wc.wait()

	require.Len(t, warnings, 1)
	require.Contains(t, string(warnings[0].Short), "Multiple platforms requested but result is not multi-platform")

	wc = newWarningsCapture()
	_, err = c.Build(sb.Context(), SolveOpt{
		FrontendAttrs: map[string]string{},
	}, "", frontend, wc.status)
	require.NoError(t, err)

	warnings = wc.wait()
	require.Len(t, warnings, 0, warningsListOutput(warnings))

	frontend = func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		res := gateway.NewResult()
		platformsToTest := []string{"linux/amd64", "linux/arm64"}
		expPlatforms := &exptypes.Platforms{
			Platforms: make([]exptypes.Platform, len(platformsToTest)),
		}
		for i, platform := range platformsToTest {
			st := llb.Scratch().File(
				llb.Mkfile("platform", 0600, []byte(platform)),
			)

			def, err := st.Marshal(ctx)
			if err != nil {
				return nil, err
			}

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

			_, err = ref.ToState()
			if err != nil {
				return nil, err
			}
			res.AddRef(platform, ref)

			expPlatforms.Platforms[i] = exptypes.Platform{
				ID:       platform,
				Platform: platforms.MustParse(platform),
			}
		}
		dt, err := json.Marshal(expPlatforms)
		if err != nil {
			return nil, err
		}
		res.AddMeta(exptypes.ExporterPlatformsKey, dt)

		return res, nil
	}

	wc = newWarningsCapture()
	_, err = c.Build(sb.Context(), SolveOpt{
		FrontendAttrs: map[string]string{
			"platform": "linux/amd64,linux/arm64",
		},
	}, "", frontend, wc.status)
	require.NoError(t, err)
	warnings = wc.wait()

	require.Len(t, warnings, 0)

	wc = newWarningsCapture()
	_, err = c.Build(sb.Context(), SolveOpt{
		FrontendAttrs: map[string]string{},
	}, "", frontend, wc.status)
	require.NoError(t, err)

	warnings = wc.wait()
	require.Len(t, warnings, 1)
	require.Contains(t, string(warnings[0].Short), "do not match result platforms linux/amd64,linux/arm64")
}
