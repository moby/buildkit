package dockerfile

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/containerd/continuity/fs/fstest"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/frontend/dockerui"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/moby/buildkit/util/testutil/workers"
	"github.com/stretchr/testify/require"
	"github.com/tonistiigi/fsutil"
)

func testWorkdirCreatesDir(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM scratch
WORKDIR /foo
WORKDIR /
`,
		`
FROM nanoserver
WORKDIR /foo
WORKDIR /
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

	fi, err := os.Lstat(filepath.Join(destDir, "foo"))
	require.NoError(t, err)
	require.Equal(t, true, fi.IsDir())
}

// testWorkdirSourceDateEpochReproducible ensures that WORKDIR is reproducible with SOURCE_DATE_EPOCH.
func testWorkdirSourceDateEpochReproducible(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	workers.CheckFeatureCompat(t, sb, workers.FeatureOCIExporter, workers.FeatureSourceDateEpoch)
	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM alpine
WORKDIR /mydir
`)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	destDir1 := t.TempDir()
	epoch := fmt.Sprintf("%d", time.Date(2023, 1, 10, 15, 34, 56, 0, time.UTC).Unix())

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"build-arg:SOURCE_DATE_EPOCH": epoch,
		},
		Exports: []client.ExportEntry{
			{
				Type:      client.ExporterOCI,
				OutputDir: destDir1,
				Attrs: map[string]string{
					"tar": "false",
				},
			},
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)

	index1, err := os.ReadFile(filepath.Join(destDir1, "index.json"))
	require.NoError(t, err)

	// Prune all cache
	ensurePruneAll(t, c, sb)

	time.Sleep(3 * time.Second)

	destDir2 := t.TempDir()
	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"build-arg:SOURCE_DATE_EPOCH": epoch,
		},
		Exports: []client.ExportEntry{
			{
				Type:      client.ExporterOCI,
				OutputDir: destDir2,
				Attrs: map[string]string{
					"tar": "false",
				},
			},
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)

	index2, err := os.ReadFile(filepath.Join(destDir2, "index.json"))
	require.NoError(t, err)

	require.Equal(t, index1, index2)
}

func testWorkdirUser(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM busybox
RUN adduser -D user
USER user
WORKDIR /mydir
RUN [ "$(stat -c "%U %G" /mydir)" == "user user" ]
`,
		`
FROM nanoserver
USER ContainerAdministrator
WORKDIR \mydir
RUN  icacls \mydir | findstr Administrators >nul || exit /b 1
`,
	))

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
	require.NoError(t, err)
}

func testWorkdirCopyIgnoreRelative(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	baseImage := integration.UnixOrWindows("scratch", "nanoserver:latest")
	dockerfile := fmt.Appendf(nil, `
FROM %[1]s AS base
WORKDIR /foo
COPY Dockerfile /
FROM %[1]s
# relative path still loaded as absolute
COPY --from=base Dockerfile .
`, baseImage)

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
	require.NoError(t, err)
}

func testWorkdirExists(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM busybox
RUN adduser -D user
RUN mkdir /mydir && chown user:user /mydir
WORKDIR /mydir
RUN [ "$(stat -c "%U %G" /mydir)" == "user user" ]
`,
		`
FROM nanoserver
USER ContainerAdministrator
RUN mkdir \mydir
RUN net user testuser Password!2345!@# /add /y
RUN icacls \mydir /grant testuser:F
WORKDIR /mydir
RUN (icacls \mydir | findstr "testuser" >nul) || exit /b 1
`,
	))

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
	require.NoError(t, err)
}
