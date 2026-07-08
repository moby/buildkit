package dockerfile

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/containerd/continuity/fs/fstest"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/frontend/dockerui"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/stretchr/testify/require"
	"github.com/tonistiigi/fsutil"
)

func testMultiStageImplicitFrom(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM scratch
COPY --from=busybox /etc/passwd test
`, `
FROM nanoserver AS build
USER ContainerAdministrator
RUN echo test> test

FROM nanoserver
COPY --from=build /test /test
`,
	))

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

	dt, err := os.ReadFile(filepath.Join(destDir, "test"))
	require.NoError(t, err)
	require.Contains(t, string(dt), integration.UnixOrWindows("root", "test"))

	// testing masked image will load actual stage

	dockerfile = []byte(integration.UnixOrWindows(
		`
FROM busybox AS golang
RUN mkdir -p /usr/bin && echo -n foo > /usr/bin/go

FROM scratch
COPY --from=golang /usr/bin/go go
`, `
FROM nanoserver AS golang
USER ContainerAdministrator
RUN  echo foo> go

FROM nanoserver
COPY --from=golang /go /go
`,
	))

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

	dt, err = os.ReadFile(filepath.Join(destDir, "go"))
	require.NoError(t, err)
	require.Contains(t, string(dt), "foo")
}

func testMultiStageCaseInsensitive(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfileStr := `
FROM %s AS STAge0
COPY foo bar
FROM %s AS staGE1
COPY --from=staGE0 bar baz
FROM %s
COPY --from=stage1 baz bax
`
	baseImage := integration.UnixOrWindows("scratch", "nanoserver")
	dockerfile := fmt.Appendf(nil, dockerfileStr, baseImage, baseImage, baseImage)
	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile("foo", []byte("foo-contents"), 0600),
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
		FrontendAttrs: map[string]string{
			"target": "Stage1",
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "baz"))
	require.NoError(t, err)
	require.Contains(t, string(dt), "foo-contents")
}

func testOutOfOrderStage(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)
	dockerfile := integration.UnixOrWindows(
		`
FROM busybox AS target
COPY --from=build %s /out

FROM alpine AS build
COPY /Dockerfile /d2

FROM target
`,
		`
FROM nanoserver AS target
COPY --from=build %s /out

FROM nanoserver AS build
COPY /Dockerfile /d2

FROM target
`,
	)
	for _, src := range []string{"/", "/d2"} {
		dockerfile := fmt.Appendf(nil, dockerfile, src)

		dir := integration.Tmpdir(
			t,
			fstest.CreateFile("Dockerfile", dockerfile, 0600),
		)

		c, err := client.New(sb.Context(), sb.Address())
		require.NoError(t, err)
		defer c.Close()

		_, err = f.Solve(sb.Context(), c, client.SolveOpt{
			LocalMounts: map[string]fsutil.FS{
				dockerui.DefaultLocalNameDockerfile: dir,
				dockerui.DefaultLocalNameContext:    dir,
			},
		}, nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "cannot copy from stage")
		require.Contains(t, err.Error(), "needs to be defined before current stage")
	}
}

func testEmptyStages(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)
	dockerfile := []byte(`ARG foo=bar`)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	destDir := t.TempDir()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
		Exports: []client.ExportEntry{
			{
				Type:      client.ExporterLocal,
				OutputDir: destDir,
			},
		},
	}, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "dockerfile contains no stages to build")
}
