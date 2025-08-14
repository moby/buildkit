package dockerfile

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"testing"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/plugins/content/local"
	"github.com/containerd/continuity/fs/fstest"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/frontend/dockerui"
	"github.com/moby/buildkit/util/testutil"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/moby/buildkit/util/testutil/workers"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	"github.com/tonistiigi/fsutil"
)

func init() {
	allTests = append(allTests, integration.TestFuncs(
		testOnBuildCleared,
		testOnBuildWithChildStage,
		testOnBuildInheritedStageRun,
		testOnBuildInheritedStageWithFrom,
		testOnBuildNewDeps,
		testOnBuildNamedContext,
		testOnBuildWithCacheMount,
	)...)
}

func testOnBuildCleared(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureDirectPush)
	f := getFrontend(t, sb)

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM busybox
ONBUILD RUN mkdir -p /out && echo -n 11 >> /out/foo
`, `
FROM nanoserver
USER ContainerAdministrator
ONBUILD RUN mkdir \out && echo 11>> \out\foo
`,
	))

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	target := registry + "/buildkit/testonbuild:base"

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		Exports: []client.ExportEntry{
			{
				Type: client.ExporterImage,
				Attrs: map[string]string{
					"push": "true",
					"name": target,
				},
			},
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)

	dockerfile = fmt.Appendf(nil, `
	FROM %s 
	`, target)

	dir = integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	target2 := registry + "/buildkit/testonbuild:child"

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		Exports: []client.ExportEntry{
			{
				Type: client.ExporterImage,
				Attrs: map[string]string{
					"push": "true",
					"name": target2,
				},
			},
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)

	dockerfile = fmt.Appendf(nil, `
	FROM %s AS base
	FROM %s
	COPY --from=base /out /
	`, target2, integration.UnixOrWindows("scratch", "nanoserver"))

	dir = integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)
	destDir := t.TempDir()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		Exports: []client.ExportEntry{
			{
				Type:      client.ExporterLocal,
				OutputDir: destDir,
			},
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "foo"))
	require.NoError(t, err)
	require.Equal(t, integration.UnixOrWindows("11", "11\r\n"), string(dt))
}

// testOnBuildWithChildStage tests that ONBUILD rules from the parent image do
// not run again if another stage inherits from current stage.
// moby/buildkit#5578
func testOnBuildWithChildStage(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	workers.CheckFeatureCompat(t, sb, workers.FeatureDirectPush)
	f := getFrontend(t, sb)

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	dockerfile := []byte(`
FROM busybox
ONBUILD RUN mkdir -p /out && echo -n yes >> /out/didrun
`)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	target := registry + "/buildkit/testonbuildstage:base"

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		Exports: []client.ExportEntry{
			{
				Type: client.ExporterImage,
				Attrs: map[string]string{
					"push": "true",
					"name": target,
				},
			},
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)

	dockerfile = fmt.Appendf(nil, `
FROM %s AS base
RUN [ -f /out/didrun ] && touch /step1
RUN rm /out/didrun
RUN [ ! -f /out/didrun ] && touch /step2

FROM base AS child
RUN [ ! -f /out/didrun ] && touch /step3

FROM scratch
COPY --from=child /step* /
	`, target)

	dir = integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)
	destDir := t.TempDir()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		Exports: []client.ExportEntry{
			{
				Type:      client.ExporterLocal,
				OutputDir: destDir,
			},
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)

	_, err = os.Stat(filepath.Join(destDir, "step1"))
	require.NoError(t, err)

	_, err = os.Stat(filepath.Join(destDir, "step2"))
	require.NoError(t, err)

	_, err = os.Stat(filepath.Join(destDir, "step3"))
	require.NoError(t, err)
}

func testOnBuildNamedContext(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	workers.CheckFeatureCompat(t, sb, workers.FeatureOCIExporter, workers.FeatureOCILayout)
	// create an image with onbuild that relies on "otherstage" when imported
	ctx := sb.Context()

	c, err := client.New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	// create a tempdir where we will store the OCI layout
	ocidir := t.TempDir()

	ociDockerfile := []byte(`
	FROM busybox:latest
	ONBUILD COPY --from=otherstage /testfile /out/foo
	`)
	inDir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", ociDockerfile, 0600),
	)

	f := getFrontend(t, sb)

	outW := bytes.NewBuffer(nil)

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: inDir,
			dockerui.DefaultLocalNameContext:    inDir,
		},
		Exports: []client.ExportEntry{
			{
				Type:   client.ExporterOCI,
				Output: fixedWriteCloser(nopWriteCloser{outW}),
			},
		},
	}, nil)
	require.NoError(t, err)

	// extract the tar stream to the directory as OCI layout
	m, err := testutil.ReadTarToMap(outW.Bytes(), false)
	require.NoError(t, err)

	for filename, content := range m {
		fullFilename := path.Join(ocidir, filename)
		err = os.MkdirAll(path.Dir(fullFilename), 0755)
		require.NoError(t, err)
		if content.Header.FileInfo().IsDir() {
			err = os.MkdirAll(fullFilename, 0755)
			require.NoError(t, err)
		} else {
			err = os.WriteFile(fullFilename, content.Data, 0644)
			require.NoError(t, err)
		}
	}

	var index ocispecs.Index
	err = json.Unmarshal(m[ocispecs.ImageIndexFile].Data, &index)
	require.NoError(t, err)
	require.Equal(t, 1, len(index.Manifests))
	digest := index.Manifests[0].Digest.Hex()

	store, err := local.NewStore(ocidir)
	ociID := "ocione"
	require.NoError(t, err)

	dockerfile := []byte(`
	FROM alpine AS otherstage
	RUN echo -n "hello" > /testfile
	
	FROM base AS inputstage
	
	FROM scratch
	COPY --from=inputstage /out/foo /bar
`)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	destDir := t.TempDir()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"context:base": fmt.Sprintf("oci-layout:%s@sha256:%s", ociID, digest),
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
		OCIStores: map[string]content.Store{
			ociID: store,
		},
		Exports: []client.ExportEntry{
			{
				Type:      client.ExporterLocal,
				OutputDir: destDir,
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "bar"))
	require.NoError(t, err)
	require.Equal(t, []byte("hello"), dt)
}

func testOnBuildInheritedStageRun(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	workers.CheckFeatureCompat(t, sb, workers.FeatureDirectPush)
	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM busybox AS base
ONBUILD RUN mkdir -p /out && echo -n 11 >> /out/foo

FROM base AS mid
RUN cp /out/foo /out/bar

FROM scratch
COPY --from=mid /out/bar /
`)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	destDir := t.TempDir()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		Exports: []client.ExportEntry{
			{
				Type:      client.ExporterLocal,
				OutputDir: destDir,
			},
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "bar"))
	require.NoError(t, err)
	require.Equal(t, "11", string(dt))
}

func testOnBuildInheritedStageWithFrom(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	workers.CheckFeatureCompat(t, sb, workers.FeatureDirectPush)
	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM alpine AS src
RUN mkdir -p /in && echo -n 12 > /in/file

FROM busybox AS base
ONBUILD COPY --from=src /in/file /out/foo

FROM base AS mid
RUN cp /out/foo /out/bar

FROM scratch
COPY --from=mid /out/bar /
`)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	destDir := t.TempDir()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		Exports: []client.ExportEntry{
			{
				Type:      client.ExporterLocal,
				OutputDir: destDir,
			},
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "bar"))
	require.NoError(t, err)
	require.Equal(t, "12", string(dt))
}

func testOnBuildNewDeps(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	workers.CheckFeatureCompat(t, sb, workers.FeatureDirectPush)
	f := getFrontend(t, sb)

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	dockerfile := []byte(`
FROM busybox
ONBUILD COPY --from=alpine /etc/alpine-release /out/alpine-release2
`)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	target := registry + "/buildkit/testonbuilddeps:base"

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		Exports: []client.ExportEntry{
			{
				Type: client.ExporterImage,
				Attrs: map[string]string{
					"push": "true",
					"name": target,
				},
			},
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)

	dockerfile = fmt.Appendf(nil, `
	FROM %s AS base
	RUN cat /out/alpine-release2 > /out/alpine-release3
	FROM scratch
	COPY --from=base /out /
	`, target)

	dir = integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)
	destDir := t.TempDir()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		Exports: []client.ExportEntry{
			{
				Type:      client.ExporterLocal,
				OutputDir: destDir,
			},
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "alpine-release3"))
	require.NoError(t, err)
	require.Greater(t, len(dt), 5)

	// build another onbuild image to test nested case
	dockerfile = []byte(`
FROM alpine
ONBUILD RUN --mount=type=bind,target=/in,from=inputstage mkdir /out && cat /in/foo > /out/bar && cat /in/out/alpine-release2 > /out/bar2
`)

	dir = integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	target2 := registry + "/buildkit/testonbuilddeps:base2"

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		Exports: []client.ExportEntry{
			{
				Type: client.ExporterImage,
				Attrs: map[string]string{
					"push": "true",
					"name": target2,
				},
			},
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)

	dockerfile = fmt.Appendf(nil, `
	FROM %s AS inputstage
	RUN cat /out/alpine-release2 > /out/alpine-release4
	RUN echo -n foo > /foo
	FROM %s AS base
	RUN echo -n bar3 > /out/bar3
	FROM scratch
	COPY --from=base /out /
	`, target, target2)

	dir = integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)
	destDir = t.TempDir()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		Exports: []client.ExportEntry{
			{
				Type:      client.ExporterLocal,
				OutputDir: destDir,
			},
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)

	dt, err = os.ReadFile(filepath.Join(destDir, "bar"))
	require.NoError(t, err)
	require.Equal(t, "foo", string(dt))

	dt, err = os.ReadFile(filepath.Join(destDir, "bar2"))
	require.NoError(t, err)
	require.Greater(t, len(dt), 5)

	dt, err = os.ReadFile(filepath.Join(destDir, "bar3"))
	require.NoError(t, err)
	require.Equal(t, "bar3", string(dt))
}

func testOnBuildWithCacheMount(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	workers.CheckFeatureCompat(t, sb, workers.FeatureDirectPush)
	f := getFrontend(t, sb)

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	dockerfile := []byte(`
FROM busybox
ONBUILD RUN --mount=type=cache,target=/cache echo -n 42 >> /cache/foo && echo -n 11 >> /bar
`)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	target := registry + "/buildkit/testonbuild:base"

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		Exports: []client.ExportEntry{
			{
				Type: client.ExporterImage,
				Attrs: map[string]string{
					"push": "true",
					"name": target,
				},
			},
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)

	dockerfile = fmt.Appendf(nil, `FROM %s
RUN --mount=type=cache,target=/cache [ "$(cat /cache/foo)" = "42" ] && [ "$(cat /bar)" = "11" ]
	`, target)

	dir = integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)
}
