package client

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/platforms"
	controlapi "github.com/moby/buildkit/api/services/control"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	gatewaypb "github.com/moby/buildkit/frontend/gateway/pb"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/exporter"
	"github.com/moby/buildkit/session/exporter/exporterprovider"
	"github.com/moby/buildkit/session/filesync"
	"github.com/moby/buildkit/util/iohelper"
	"github.com/moby/buildkit/util/testutil"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/moby/buildkit/util/testutil/workers"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func testExportLocalForcePlatformSplit(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureOCIExporter, workers.FeatureMultiPlatform)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	frontend := func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		st := llb.Scratch().File(
			llb.Mkfile("foo", 0600, []byte("hello")),
		)

		def, err := st.Marshal(ctx)
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
				Attrs: map[string]string{
					"platform-split": "true",
				},
			},
		},
	}, "", frontend, nil)
	require.NoError(t, err)

	fis, err := os.ReadDir(destDir)
	require.NoError(t, err)

	require.Len(t, fis, 1, "expected one files in the output directory")

	expPlatform := strings.ReplaceAll(platforms.FormatAll(platforms.Normalize(platforms.DefaultSpec())), "/", "_")
	_, err = os.Stat(filepath.Join(destDir, expPlatform+"/"))
	require.NoError(t, err)

	fis, err = os.ReadDir(filepath.Join(destDir, expPlatform))
	require.NoError(t, err)

	require.Len(t, fis, 1, "expected one files in the output directory for platform "+expPlatform)

	dt, err := os.ReadFile(filepath.Join(destDir, expPlatform, "foo"))
	require.NoError(t, err)
	require.Equal(t, "hello", string(dt))
}

func testExportLocalModeCopyKeepsStaleDestinationFiles(t *testing.T, sb integration.Sandbox) {
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	st := llb.Scratch().File(
		llb.Mkfile("fresh.txt", 0600, []byte("fresh")),
	)
	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	destDir := t.TempDir()
	err = os.WriteFile(filepath.Join(destDir, "stale.txt"), []byte("stale"), 0600)
	require.NoError(t, err)
	err = os.MkdirAll(filepath.Join(destDir, "stale-dir"), 0755)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(destDir, "stale-dir", "old.txt"), []byte("stale"), 0600)
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "fresh.txt"))
	require.NoError(t, err)
	require.Equal(t, "fresh", string(dt))

	_, err = os.Stat(filepath.Join(destDir, "stale.txt"))
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(destDir, "stale-dir", "old.txt"))
	require.NoError(t, err)
}

func testExportLocalModeCopyMultiPlatformKeepsAllPlatforms(t *testing.T, sb integration.Sandbox) {
	testExportLocalModeMultiPlatformKeepsAllPlatforms(t, sb, false)
}

func testExportLocalModeDeleteRemovesStaleDestinationFiles(t *testing.T, sb integration.Sandbox) {
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	st := llb.Scratch().File(
		llb.Mkfile("fresh.txt", 0600, []byte("fresh")),
	)
	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	destDir := t.TempDir()
	err = os.WriteFile(filepath.Join(destDir, "stale.txt"), []byte("stale"), 0600)
	require.NoError(t, err)
	err = os.MkdirAll(filepath.Join(destDir, "stale-dir"), 0755)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(destDir, "stale-dir", "old.txt"), []byte("stale"), 0600)
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
				Attrs: map[string]string{
					"mode": "delete",
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "fresh.txt"))
	require.NoError(t, err)
	require.Equal(t, "fresh", string(dt))

	_, err = os.Stat(filepath.Join(destDir, "stale.txt"))
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = os.Stat(filepath.Join(destDir, "stale-dir"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func testExportLocalModeDeleteMultiPlatformKeepsAllPlatforms(t *testing.T, sb integration.Sandbox) {
	testExportLocalModeMultiPlatformKeepsAllPlatforms(t, sb, true)
}

func testExportLocalModeInvalid(t *testing.T, sb integration.Sandbox) {
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	st := llb.Scratch().File(
		llb.Mkfile("fresh.txt", 0600, []byte("fresh")),
	)
	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	destDir := t.TempDir()
	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
				Attrs: map[string]string{
					"mode": "backup",
				},
			},
		},
	}, nil)
	require.Error(t, err)
	require.ErrorContains(t, err, `invalid local exporter mode "backup"`)
}

func testExportLocalModeMultiPlatformKeepsAllPlatforms(t *testing.T, sb integration.Sandbox, deleteMode bool) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureOCIExporter, workers.FeatureMultiPlatform)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	const filesPerPlatform = 20
	platformsToTest := []string{"linux/amd64", "linux/arm64", "linux/arm/v7", "linux/s390x"}

	frontend := func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		res := gateway.NewResult()
		expPlatforms := &exptypes.Platforms{
			Platforms: make([]exptypes.Platform, len(platformsToTest)),
		}
		for i, platform := range platformsToTest {
			st := llb.Scratch()
			for j := range filesPerPlatform {
				st = st.File(
					llb.Mkfile(fmt.Sprintf("file-%03d.txt", j), 0600, fmt.Appendf(nil, "%s-%d", platform, j)),
				)
			}

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

	destDir := t.TempDir()

	// Pre-populate dest with directories matching the platform-split naming
	// convention. Delete mode must remove stale files from all platform
	// directories while preserving every platform's fresh output.
	err = os.WriteFile(filepath.Join(destDir, "stale.txt"), []byte("stale"), 0600)
	require.NoError(t, err)
	for _, platform := range platformsToTest {
		platDir := filepath.Join(destDir, strings.ReplaceAll(platform, "/", "_"))
		err = os.MkdirAll(platDir, 0755)
		require.NoError(t, err)
		for j := range filesPerPlatform {
			err = os.WriteFile(filepath.Join(platDir, fmt.Sprintf("old-%03d.txt", j)), []byte("old"), 0600)
			require.NoError(t, err)
		}
	}

	attrs := map[string]string{}
	if deleteMode {
		attrs["mode"] = "delete"
	}

	_, err = c.Build(sb.Context(), SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
				Attrs:     attrs,
			},
		},
	}, "", frontend, nil)
	require.NoError(t, err)

	if deleteMode {
		// Stale top-level file must be gone.
		_, err = os.Stat(filepath.Join(destDir, "stale.txt"))
		require.ErrorIs(t, err, os.ErrNotExist)
	}

	// Every platform's build output must survive (no cross-platform deletion).
	for _, platform := range platformsToTest {
		platformDir := filepath.Join(destDir, strings.ReplaceAll(platform, "/", "_"))
		for j := range filesPerPlatform {
			dt, err := os.ReadFile(filepath.Join(platformDir, fmt.Sprintf("file-%03d.txt", j)))
			require.NoError(t, err, "missing build output file-%03d.txt for %s", j, platform)
			require.Equal(t, fmt.Sprintf("%s-%d", platform, j), string(dt))
		}
		if deleteMode {
			// Pre-existing files within each platform dir must be cleaned up.
			_, err = os.Stat(filepath.Join(platformDir, "old-000.txt"))
			require.ErrorIs(t, err, os.ErrNotExist, "stale file not removed for %s", platform)
		}
	}
}

func testExportLocalNoPlatformSplit(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureOCIExporter, workers.FeatureMultiPlatform)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	platformsToTest := []string{"linux/amd64", "linux/arm64"}
	frontend := func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		res := gateway.NewResult()
		expPlatforms := &exptypes.Platforms{
			Platforms: make([]exptypes.Platform, len(platformsToTest)),
		}
		for i, platform := range platformsToTest {
			st := llb.Scratch().File(
				llb.Mkfile("hello-"+strings.ReplaceAll(platform, "/", "-"), 0600, []byte(platform)),
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

	destDir := t.TempDir()
	_, err = c.Build(sb.Context(), SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
				Attrs: map[string]string{
					"platform-split": "false",
				},
			},
		},
	}, "", frontend, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "hello-linux-amd64"))
	require.NoError(t, err)
	require.Equal(t, "linux/amd64", string(dt))

	dt, err = os.ReadFile(filepath.Join(destDir, "hello-linux-arm64"))
	require.NoError(t, err)
	require.Equal(t, "linux/arm64", string(dt))
}

func testExportLocalNoPlatformSplitOverwrite(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureOCIExporter, workers.FeatureMultiPlatform)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	platformsToTest := []string{"linux/amd64", "linux/arm64"}
	frontend := func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		res := gateway.NewResult()
		expPlatforms := &exptypes.Platforms{
			Platforms: make([]exptypes.Platform, len(platformsToTest)),
		}
		for i, platform := range platformsToTest {
			st := llb.Scratch().File(
				llb.Mkfile("hello-linux", 0600, []byte(platform)),
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

	destDir := t.TempDir()
	_, err = c.Build(sb.Context(), SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
				Attrs: map[string]string{
					"platform-split": "false",
				},
			},
		},
	}, "", frontend, nil)
	require.Error(t, err)
	require.ErrorContains(t, err, "cannot overwrite hello-linux from")
	require.ErrorContains(t, err, "when split option is disabled")
}

func testExporterTargetExists(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureOCIExporter)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	imgName := integration.UnixOrWindows("busybox:latest", "nanoserver:latest")
	st := llb.Image(imgName)
	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	var mdDgst string
	res, err := c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:  ExporterOCI,
				Attrs: map[string]string{},
				Output: func(m map[string]string) (io.WriteCloser, error) {
					mdDgst = m[exptypes.ExporterImageDigestKey]
					return nil, nil
				},
			},
		},
	}, nil)
	require.NoError(t, err)
	dgst := res.ExporterResponse[exptypes.ExporterImageDigestKey]

	require.True(t, strings.HasPrefix(dgst, "sha256:"))
	require.Equal(t, dgst, mdDgst)

	require.True(t, strings.HasPrefix(res.ExporterResponse[exptypes.ExporterImageConfigDigestKey], "sha256:"))
}

func testMultipleExporters(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)

	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	def, err := llb.Scratch().File(llb.Mkfile("foo.txt", 0o755, nil)).Marshal(context.TODO())
	require.NoError(t, err)

	destDir, destDir2 := t.TempDir(), t.TempDir()
	out := filepath.Join(destDir, "out.tar")
	outW, err := os.Create(out)
	require.NoError(t, err)
	defer outW.Close()

	out2 := filepath.Join(destDir, "out2.tar")
	outW2, err := os.Create(out2)
	require.NoError(t, err)
	defer outW2.Close()

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	target1, target2 := registry+"/buildkit/build/exporter:image",
		registry+"/buildkit/build/alternative:image"

	var exporters []ExportEntry
	if workers.IsTestDockerd() {
		exporters = append(exporters, ExportEntry{
			Type: "moby",
			Attrs: map[string]string{
				"name": strings.Join([]string{target1, target2}, ","),
			},
		})
	} else {
		exporters = append(exporters, []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name": target1,
				},
			},
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name":           target2,
					"oci-mediatypes": "false",
				},
			},
		}...)
	}

	exporters = append(exporters, []ExportEntry{
		// Ensure that multiple local exporter destinations are written properly
		{
			Type:      ExporterLocal,
			OutputDir: destDir,
		},
		{
			Type:      ExporterLocal,
			OutputDir: destDir2,
		},
		// Ensure that multiple instances of the same exporter are possible
		{
			Type:   ExporterTar,
			Output: fixedWriteCloser(outW),
		},
		{
			Type:   ExporterTar,
			Output: fixedWriteCloser(outW2),
		},
	}...)

	ref := identity.NewID()
	resp, err := c.Solve(sb.Context(), def, SolveOpt{
		Ref:     ref,
		Exports: exporters,
	}, nil)
	require.NoError(t, err)

	if workers.IsTestDockerd() {
		require.Equal(t, target1+","+target2, resp.ExporterResponse[exptypes.ExporterImageNameKey])
	} else {
		require.Equal(t, target2, resp.ExporterResponse[exptypes.ExporterImageNameKey])
	}
	require.FileExists(t, filepath.Join(destDir, "out.tar"))
	require.FileExists(t, filepath.Join(destDir, "out2.tar"))
	require.FileExists(t, filepath.Join(destDir, "foo.txt"))
	require.FileExists(t, filepath.Join(destDir2, "foo.txt"))

	history, err := c.ControlClient().ListenBuildHistory(sb.Context(), &controlapi.BuildHistoryRequest{
		Ref:       ref,
		EarlyExit: true,
	})
	require.NoError(t, err)
	for {
		ev, err := history.Recv()
		if err != nil {
			require.Equal(t, io.EOF, err)
			break
		}
		require.Equal(t, ref, ev.Record.Ref)

		if workers.IsTestDockerd() {
			require.Len(t, ev.Record.Result.Results, 1)
			require.Len(t, ev.Record.Exporters, 5)
			if workers.IsTestDockerdMoby(sb) {
				require.Equal(t, ocispecs.MediaTypeImageConfig, ev.Record.Result.Results[0].MediaType)
			} else {
				require.Equal(t, ocispecs.MediaTypeImageManifest, ev.Record.Result.Results[0].MediaType)
			}
		} else {
			require.Len(t, ev.Record.Result.Results, 2)
			require.Len(t, ev.Record.Exporters, 6)
			require.Equal(t, ocispecs.MediaTypeImageManifest, ev.Record.Result.Results[0].MediaType)
			require.Equal(t, images.MediaTypeDockerSchema2Manifest, ev.Record.Result.Results[1].MediaType)
		}
		require.Equal(t, ev.Record.Result.Results[0], ev.Record.Result.ResultDeprecated)
	}
}

// TODO: Re-enable the skipped test on Windows (see issue #6296)
func testSessionExporter(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows", "This test passed locally on windows, but failed on github action")
	workers.CheckFeatureCompat(t, sb, workers.FeatureOCIExporter, workers.FeatureOCILayout)
	c, err := New(context.TODO(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	// make an image that is exported there
	imgName := integration.UnixOrWindows("busybox:latest", "nanoserver:latest")
	busybox := llb.Image(imgName)
	st := integration.UnixOrWindows(
		llb.Scratch(),
		llb.Image(imgName),
	)

	run := func(cmd string) {
		st = busybox.Run(llb.Shlex(cmd), llb.Dir("/wd")).AddMount("/wd", st)
	}

	cmdPrefix := integration.UnixOrWindows(
		`sh -c "echo -n`,
		`cmd /C "echo`,
	)
	run(fmt.Sprintf(`%s first > foo"`, cmdPrefix))
	run(fmt.Sprintf(`%s second > bar"`, cmdPrefix))

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	var attachable []session.Attachable
	target := filesync.NewFSSyncTarget()

	attachable = append(attachable, target)

	buildID := identity.NewID()

	outW := bytes.NewBuffer(nil)
	exporterCalled := false
	// The provider has no finalization callback; FinalizeExport returning
	// Unimplemented must not fail the solve.
	exporter := exporterprovider.New(func(ctx context.Context, md map[string][]byte, refs []string) ([]*exporter.ExporterRequest, error) {
		require.Len(t, refs, 1)
		g := c.GatewayClientForBuild(buildID)
		resp, err := g.ReadDir(sb.Context(), &gatewaypb.ReadDirRequest{
			Ref:     refs[0],
			DirPath: "/",
		})
		if err != nil {
			return nil, err
		}
		t.Logf("readdir: %v", resp)

		require.Equal(t, 2, len(resp.Entries))
		require.Equal(t, "bar", resp.Entries[0].Path)
		require.Equal(t, "foo", resp.Entries[1].Path)

		exporterCalled = true
		target.Add(filesync.WithFSSync(0, fixedWriteCloser(&iohelper.NopWriteCloser{Writer: outW})))
		return []*exporter.ExporterRequest{
			{
				Type: ExporterOCI,
				Attrs: map[string]string{
					"compression": "zstd",
				},
			},
		}, nil
	})
	require.NoError(t, err)
	attachable = append(attachable, exporter)

	_, err = c.Build(sb.Context(), SolveOpt{
		EnableSessionExporter: true,
		Session:               attachable,
		Ref:                   buildID,
	}, "", func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		return c.Solve(ctx, gateway.SolveRequest{
			Definition: def.ToPB(),
		})
	}, nil)
	require.NoError(t, err)

	require.True(t, exporterCalled)

	// extract the tar stream to the directory as OCI layout
	m, err := testutil.ReadTarToMap(outW.Bytes(), false)
	require.NoError(t, err)

	var index ocispecs.Index
	err = json.Unmarshal(m[ocispecs.ImageIndexFile].Data, &index)
	require.NoError(t, err)
	require.Equal(t, 1, len(index.Manifests))
	digest := index.Manifests[0].Digest

	var mfst ocispecs.Manifest
	err = json.Unmarshal(m[path.Join("blobs/sha256", digest.Hex())].Data, &mfst)
	require.NoError(t, err)

	if runtime.GOOS == "windows" {
		mfst.Layers = mfst.Layers[1:] // skip base layer
	}

	require.Equal(t, 2, len(mfst.Layers))
	for _, layer := range mfst.Layers {
		require.Contains(t, layer.MediaType, ocispecs.MediaTypeImageLayerZstd)
	}
}

func testSessionExporterFinalizeExport(t *testing.T, sb integration.Sandbox) {
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	st := llb.Image(integration.UnixOrWindows("busybox:latest", "nanoserver:latest"))
	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	build := func(name string, p session.Attachable) error {
		_, err := c.Build(sb.Context(), SolveOpt{
			EnableSessionExporter: true,
			Exports: []ExportEntry{
				{
					Type: ExporterImage,
					Attrs: map[string]string{
						"name": "session-exporter-finalize-" + name,
					},
				},
			},
			Session: []session.Attachable{p},
			Ref:     identity.NewID(),
		}, "", func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
			return c.Solve(ctx, gateway.SolveRequest{
				Definition: def.ToPB(),
			})
		}, nil)
		return err
	}

	callbackErr := errors.New("finalize callback failed")
	for _, tc := range []struct {
		name        string
		callbackErr error
		wantErr     bool
	}{
		{name: "success"},
		{name: "unavailable", callbackErr: status.Error(codes.Unavailable, "finalize callback unavailable")},
		{name: "error", callbackErr: callbackErr, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			p := exporterprovider.New(nil, exporterprovider.WithFinalizeCallback(func(_ context.Context, exporterResponse map[string]string) error {
				called = true
				assert.NotEmpty(t, exporterResponse[exptypes.ExporterImageDescriptorKey])
				return tc.callbackErr
			}))

			err := build(tc.name, p)
			if tc.wantErr {
				require.ErrorContains(t, err, tc.callbackErr.Error())
			} else {
				require.NoError(t, err)
			}
			assert.True(t, called)
		})
	}
}

// moby/buildkit#1418
func testTarExporterSymlink(t *testing.T, sb integration.Sandbox) {
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	imgName := integration.UnixOrWindows("busybox:latest", "nanoserver:latest")
	busybox := llb.Image(imgName)
	st := integration.UnixOrWindows(llb.Scratch(), llb.Image(imgName))

	run := func(cmd string) {
		st = busybox.Run(llb.Shlex(cmd), llb.Dir("/wd")).AddMount("/wd", st)
	}

	cmdStr := integration.UnixOrWindows(
		`sh -c "echo -n first > foo;ln -s foo bar"`,
		`cmd /C "echo first> foo && mklink bar foo"`,
	)
	run(cmdStr)

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	var buf bytes.Buffer
	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:   ExporterTar,
				Output: fixedWriteCloser(&iohelper.NopWriteCloser{Writer: &buf}),
			},
		},
	}, nil)
	require.NoError(t, err)

	m, err := testutil.ReadTarToMap(buf.Bytes(), false)
	require.NoError(t, err)

	item, ok := m["foo"]
	require.True(t, ok)
	require.Equal(t, tar.TypeReg, int32(item.Header.Typeflag))
	newLine := integration.UnixOrWindows("", " \r\n")
	require.Equal(t, []byte("first"+newLine), item.Data)

	item, ok = m["bar"]
	require.True(t, ok)
	require.Equal(t, tar.TypeSymlink, int32(item.Header.Typeflag))
	require.Equal(t, "foo", item.Header.Linkname)
}

func testTarExporterWithSocket(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	alpine := llb.Image("docker.io/library/alpine:latest")
	def, err := alpine.Run(llb.Args([]string{"sh", "-c", "nc -l -s local:/socket.sock & usleep 100000; kill %1"})).Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:  ExporterTar,
				Attrs: map[string]string{},
				Output: func(m map[string]string) (io.WriteCloser, error) {
					return &iohelper.NopWriteCloser{Writer: io.Discard}, nil
				},
			},
		},
	}, nil)
	require.NoError(t, err)
}

func testTarExporterWithSocketCopy(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	alpine := llb.Image("docker.io/library/alpine:latest")
	state := alpine.Run(llb.Args([]string{"sh", "-c", "nc -l -s local:/root/socket.sock & usleep 100000; kill %1"})).Root()

	fa := llb.Copy(state, "/root", "/roo2", &llb.CopyInfo{})

	scratchCopy := llb.Scratch().File(fa)

	def, err := scratchCopy.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.NoError(t, err)
}
