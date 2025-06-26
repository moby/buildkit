package dockerfile

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	ctd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/snapshots"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/continuity/fs/fstest"
	"github.com/containerd/platforms"
	controlapi "github.com/moby/buildkit/api/services/control"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/frontend/dockerfile/builder"
	"github.com/moby/buildkit/frontend/dockerui"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/frontend/subrequests"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/filesync"
	"github.com/moby/buildkit/session/upload/uploadprovider"
	"github.com/moby/buildkit/solver/errdefs"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/contentutil"
	"github.com/moby/buildkit/util/testutil"
	"github.com/moby/buildkit/util/testutil/httpserver"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/moby/buildkit/util/testutil/workers"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	"github.com/tonistiigi/fsutil"
	"golang.org/x/sync/errgroup"
)

func init() {
	if workers.IsTestDockerd() {
		workers.InitDockerdWorker()
	} else {
		workers.InitOCIWorker()
		workers.InitContainerdWorker()
	}
}

var allTests = integration.TestFuncs(
	testCmdShell,
	testGlobalArg,
	testDirs,
	testInvalidCommand,
	testScratchConfig,
	testExposeExpansion,
	testDockerignore,
	testDockerignoreInvalid,
	testDockerfileFromGit,
	testMultiStageImplicitFrom,
	testMultiStageCaseInsensitive,
	testLabels,
	testReproducibleIDs,
	testImportExportReproducibleIDs,
	testDockerfileFromHTTP,
	testBuiltinArgs,
	testPullScratch,
	testSymlinkDestination,
	testHTTPDockerfile,
	testPlatformArgsImplicit,
	testPlatformArgsExplicit,
	testExportMultiPlatform,
	testQuotedMetaArgs,
	testGlobalArgErrors,
	testArgDefaultExpansion,
	testIgnoreEntrypoint,
	testSymlinkedDockerfile,
	testWorkdirCreatesDir,
	testDockerignoreOverride,
	testTarExporterMulti,
	testTarExporterBasic,
	testDefaultEnvWithArgs,
	testEnvEmptyFormatting,
	testFrontendUseForwardedSolveResults,
	testFrontendEvaluate,
	testFrontendInputs,
	testErrorsSourceMap,
	testMultiArgs,
	testFrontendSubrequests,
	testDockerfileCheckHostname,
	testDefaultShellAndPath,
	testDockerfileLowercase,
	testDockerfileInvalidInstruction,
	testShmSize,
	testUlimit,
	testCgroupParent,
	testContextChangeDirToFile,
	testNoSnapshotLeak,
	testTarContext,
	testTarContextExternalDockerfile,
	testWorkdirUser,
	testWorkdirExists,
	testWorkdirCopyIgnoreRelative,
	testOutOfOrderStage,
	testSourceDateEpochWithoutExporter,
	testMultiNilRefsOCIExporter,
	testNilContextInSolveGateway,
	testMultiNilRefsInSolveGateway,
	testFrontendDeduplicateSources,
	testEmptyStringArgInEnv,
	testInvalidJSONCommands,
	testEmptyStages,
	testTargetStageNameArg,
	testStepNames,
	testDefaultPathEnvOnWindows,
	testOCILayoutMultiname,
	testPlatformWithOSVersion,
	testMaintainBaseOSVersion,
	testTargetMistype,
)

// Tests that depend on the `security.*` entitlements
var securityTests = []integration.Test{}

// Tests that depend on the `network.*` entitlements
var networkTests = []integration.Test{}

// Tests that depend on reproducible env
var reproTests = integration.TestFuncs(
	testReproSourceDateEpoch,
	testWorkdirSourceDateEpochReproducible,
)

var (
	opts         []integration.TestOpt
	securityOpts []integration.TestOpt
)

type frontend interface {
	Solve(context.Context, *client.Client, client.SolveOpt, chan *client.SolveStatus) (*client.SolveResponse, error)
	SolveGateway(context.Context, gateway.Client, gateway.SolveRequest) (*gateway.Result, error)
	DFCmdArgs(string, string) (string, string)
	RequiresBuildctl(t *testing.T)
}

func init() {
	frontends := map[string]any{}

	images := integration.UnixOrWindows(
		[]string{"busybox:latest", "alpine:latest", "busybox:stable-musl"},
		[]string{"nanoserver:latest", "nanoserver:plus", "nanoserver:plus-busybox"})
	opts = []integration.TestOpt{
		integration.WithMirroredImages(integration.OfficialImages(images...)),
		integration.WithMatrix("frontend", frontends),
	}

	if os.Getenv("FRONTEND_BUILTIN_ONLY") == "1" {
		frontends["builtin"] = &builtinFrontend{}
	} else if os.Getenv("FRONTEND_CLIENT_ONLY") == "1" {
		frontends["client"] = &clientFrontend{}
	} else if gw := os.Getenv("FRONTEND_GATEWAY_ONLY"); gw != "" {
		name := "buildkit_test/" + identity.NewID() + ":latest"
		opts = append(opts, integration.WithMirroredImages(map[string]string{
			name: gw,
		}))
		frontends["gateway"] = &gatewayFrontend{gw: name}
	} else {
		frontends["builtin"] = &builtinFrontend{}
		frontends["client"] = &clientFrontend{}
	}
}

func TestIntegration(t *testing.T) {
	integration.Run(t, allTests, opts...)

	// the rest of the tests are meant for non-Windows, skipping on Windows.
	integration.SkipOnPlatform(t, "windows")

	integration.Run(t, reproTests, append(opts,
		// Only use the amd64 digest,  regardless to the host platform
		integration.WithMirroredImages(map[string]string{
			"amd64/debian:bullseye-20230109-slim": "docker.io/amd64/debian:bullseye-20230109-slim@sha256:1acb06a0c31fb467eb8327ad361f1091ab265e0bf26d452dea45dcb0c0ea5e75",
		}),
	)...)

	integration.Run(t, securityTests, append(append(opts, securityOpts...),
		integration.WithMatrix("security.insecure", map[string]any{
			"granted": securityInsecureGranted,
			"denied":  securityInsecureDenied,
		}))...)

	integration.Run(t, networkTests, append(opts,
		integration.WithMatrix("network.host", map[string]any{
			"granted": networkHostGranted,
			"denied":  networkHostDenied,
		}))...)
}

func testEmptyStringArgInEnv(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM busybox AS build
ARG FOO
ARG BAR=
RUN env > env.txt

FROM scratch
COPY --from=build env.txt .
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

	dt, err := os.ReadFile(filepath.Join(destDir, "env.txt"))
	require.NoError(t, err)

	envStr := string(dt)
	require.Contains(t, envStr, "BAR=")
	require.NotContains(t, envStr, "FOO=")
}

func testDefaultEnvWithArgs(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	f := getFrontend(t, sb)

	dockerfile := []byte(`
ARG image=idlebox
FROM busy${image#idle} AS build
ARG my_arg
ENV my_arg "my_arg=${my_arg:-def_val}"
ENV my_trimmed_arg "${my_arg%%e*}"
COPY myscript.sh myscript.sh
RUN ./myscript.sh $my_arg $my_trimmed_arg
FROM scratch
COPY --from=build /out /out
`)

	script := []byte(`
#!/usr/bin/env sh
echo -n $my_arg $* > /out
`)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile("myscript.sh", script, 0700),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	destDir := t.TempDir()

	for _, x := range []struct {
		name          string
		frontendAttrs map[string]string
		expected      string
	}{
		{"nil", nil, "my_arg=def_val my_arg=def_val my_arg=d"},
		{"empty", map[string]string{"build-arg:my_arg": ""}, "my_arg=def_val my_arg=def_val my_arg=d"},
		{"override", map[string]string{"build-arg:my_arg": "override"}, "my_arg=override my_arg=override my_arg=ov"},
	} {
		t.Run(x.name, func(t *testing.T) {
			_, err = f.Solve(sb.Context(), c, client.SolveOpt{
				FrontendAttrs: x.frontendAttrs,
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

			dt, err := os.ReadFile(filepath.Join(destDir, "out"))
			require.NoError(t, err)
			require.Equal(t, x.expected, string(dt))
		})
	}
}

func testEnvEmptyFormatting(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM busybox AS build
ENV myenv foo%sbar
RUN [ "$myenv" = 'foo%sbar' ]
`,
		`
FROM nanoserver AS build
ENV myenv foo%sbar
RUN if %myenv% NEQ foo%sbar (exit 1)
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
}

func testDockerignoreOverride(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)
	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM busybox
COPY . .
RUN [ -f foo ] && [ ! -f bar ]
`,
		`
FROM nanoserver
COPY . .
RUN if exist foo (if not exist bar (exit 0) else (exit 1))
`,
	))

	ignore := []byte(`
bar
`)

	dockerfile2 := []byte(integration.UnixOrWindows(
		`
FROM busybox
COPY . .
RUN [ ! -f foo ] && [ -f bar ]
`,
		`
FROM nanoserver
COPY . .
RUN if not exist foo (if exist bar (exit 0) else (exit 1))
`,
	))

	ignore2 := []byte(`
foo
`)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile("Dockerfile.dockerignore", ignore, 0600),
		fstest.CreateFile("Dockerfile2", dockerfile2, 0600),
		fstest.CreateFile("Dockerfile2.dockerignore", ignore2, 0600),
		fstest.CreateFile("foo", []byte("contents0"), 0600),
		fstest.CreateFile("bar", []byte("contents0"), 0600),
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

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"filename": "Dockerfile2",
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)
}

func testTarExporterBasic(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM scratch
COPY foo foo
`,
		`
FROM nanoserver
COPY foo foo
`,
	))

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile("foo", []byte("data"), 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	buf := &bytes.Buffer{}
	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		Exports: []client.ExportEntry{
			{
				Type:   client.ExporterTar,
				Output: fixedWriteCloser(&nopWriteCloser{buf}),
			},
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)

	m, err := testutil.ReadTarToMap(buf.Bytes(), false)
	require.NoError(t, err)

	mi, ok := m["foo"]
	require.Equal(t, true, ok)
	require.Equal(t, "data", string(mi.Data))
}

func testTarExporterMulti(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM scratch AS stage-linux
COPY foo forlinux

FROM scratch AS stage-darwin
COPY bar fordarwin

FROM stage-$TARGETOS
`)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile("foo", []byte("data"), 0600),
		fstest.CreateFile("bar", []byte("data2"), 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	buf := &bytes.Buffer{}
	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		Exports: []client.ExportEntry{
			{
				Type:   client.ExporterTar,
				Output: fixedWriteCloser(&nopWriteCloser{buf}),
			},
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)

	m, err := testutil.ReadTarToMap(buf.Bytes(), false)
	require.NoError(t, err)

	mi, ok := m["forlinux"]
	require.Equal(t, true, ok)
	require.Equal(t, "data", string(mi.Data))

	// repeat multi-platform
	buf = &bytes.Buffer{}
	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		Exports: []client.ExportEntry{
			{
				Type:   client.ExporterTar,
				Output: fixedWriteCloser(&nopWriteCloser{buf}),
			},
		},
		FrontendAttrs: map[string]string{
			"platform": "linux/amd64,darwin/amd64",
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)

	m, err = testutil.ReadTarToMap(buf.Bytes(), false)
	require.NoError(t, err)

	mi, ok = m["linux_amd64/forlinux"]
	require.Equal(t, true, ok)
	require.Equal(t, "data", string(mi.Data))

	mi, ok = m["darwin_amd64/fordarwin"]
	require.Equal(t, true, ok)
	require.Equal(t, "data2", string(mi.Data))
}

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

func testSymlinkedDockerfile(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM scratch
ENV foo bar
`,
		`
FROM nanoserver
ENV foo bar
`,
	))

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile.web", dockerfile, 0600),
		fstest.Symlink("Dockerfile.web", "Dockerfile"),
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

func testWorkdirCopyIgnoreRelative(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM scratch AS base
WORKDIR /foo
COPY Dockerfile / 
FROM scratch
# relative path still loaded as absolute
COPY --from=base Dockerfile .
`)

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

func testIgnoreEntrypoint(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM busybox
ENTRYPOINT ["/nosuchcmd"]
RUN ["ls"]
`,
		`
FROM nanoserver AS build
ENTRYPOINT ["nosuchcmd.exe"]
RUN dir
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

func testQuotedMetaArgs(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(integration.UnixOrWindows(
		`
ARG a1="box"
ARG a2="$a1-foo"
FROM busy$a1 AS build
ARG a2
ARG a3="bar-$a2"
RUN echo -n $a3 > /out
FROM scratch
COPY --from=build /out .
`,
		`
ARG a1="server"
ARG a2="$a1-foo"
FROM nano$a1 AS build
USER ContainerAdministrator
ARG a2
ARG a3="bar-$a2"
RUN echo %a3% > /out
FROM nanoserver
COPY --from=build /out .
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
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "out"))
	require.NoError(t, err)

	testString := string([]byte(integration.UnixOrWindows("bar-box-foo", "bar-server-foo \r\n")))

	require.Equal(t, testString, string(dt))
}

func testGlobalArgErrors(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	imgName := integration.UnixOrWindows("busybox", "nanoserver")
	dockerfile := fmt.Appendf(nil, `
ARG FOO=${FOO:?"custom error"}
FROM %s
`, imgName)

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

	require.Contains(t, err.Error(), "FOO: custom error")

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"build-arg:FOO": "bar",
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)
}

func testArgDefaultExpansion(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := fmt.Appendf(nil, `
FROM %s
ARG FOO
ARG BAR=${FOO:?"foo missing"}
`, integration.UnixOrWindows("scratch", "nanoserver"))

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

	require.Contains(t, err.Error(), "FOO: foo missing")

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"build-arg:FOO": "123",
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"build-arg:BAR": "123",
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)
}

func testMultiArgs(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(integration.UnixOrWindows(
		`
ARG a1="foo bar" a2=box
ARG a3="$a2-foo"
FROM busy$a2 AS build
ARG a3 a4="123 456" a1
RUN echo -n "$a1:$a3:$a4" > /out
FROM scratch
COPY --from=build /out .
`,
		`
ARG a1="foo bar" a2=server
ARG a3="$a2-foo"
FROM nano$a2 AS build
USER ContainerAdministrator
ARG a3 a4="123 456" a1
RUN echo %a1%:%a3%:%a4%> /out
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
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "out"))
	require.NoError(t, err)
	// On Windows, echo adds \r\n on the output
	out := integration.UnixOrWindows(
		"foo bar:box-foo:123 456",
		"foo bar:server-foo:123 456\r\n",
	)
	require.Equal(t, out, string(dt))
}

func testDefaultShellAndPath(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	workers.CheckFeatureCompat(t, sb, workers.FeatureOCIExporter)
	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM scratch
ENTRYPOINT foo bar
COPY Dockerfile .
`)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	destDir := t.TempDir()

	out := filepath.Join(destDir, "out.tar")
	outW, err := os.Create(out)
	require.NoError(t, err)

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
		FrontendAttrs: map[string]string{
			"platform": "windows/amd64,linux/amd64",
		},
		Exports: []client.ExportEntry{
			{
				Type:   client.ExporterOCI,
				Output: fixedWriteCloser(outW),
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "out.tar"))
	require.NoError(t, err)

	m, err := testutil.ReadTarToMap(dt, false)
	require.NoError(t, err)

	var idx ocispecs.Index
	err = json.Unmarshal(m[ocispecs.ImageIndexFile].Data, &idx)
	require.NoError(t, err)

	mlistHex := idx.Manifests[0].Digest.Hex()

	idx = ocispecs.Index{}
	err = json.Unmarshal(m[ocispecs.ImageBlobsDir+"/sha256/"+mlistHex].Data, &idx)
	require.NoError(t, err)

	require.Equal(t, 2, len(idx.Manifests))

	for i, exp := range []struct {
		p          string
		entrypoint []string
		env        []string
	}{
		// we don't set PATH on Windows. #5445
		{p: "windows/amd64", entrypoint: []string{"cmd", "/S", "/C", "foo bar"}, env: []string(nil)},
		{p: "linux/amd64", entrypoint: []string{"/bin/sh", "-c", "foo bar"}, env: []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"}},
	} {
		t.Run(exp.p, func(t *testing.T) {
			require.Equal(t, exp.p, platforms.Format(*idx.Manifests[i].Platform))

			var mfst ocispecs.Manifest
			err = json.Unmarshal(m[ocispecs.ImageBlobsDir+"/sha256/"+idx.Manifests[i].Digest.Hex()].Data, &mfst)
			require.NoError(t, err)

			require.Equal(t, 1, len(mfst.Layers))

			var img ocispecs.Image
			err = json.Unmarshal(m[ocispecs.ImageBlobsDir+"/sha256/"+mfst.Config.Digest.Hex()].Data, &img)
			require.NoError(t, err)

			require.Equal(t, exp.entrypoint, img.Config.Entrypoint)
			require.Equal(t, exp.env, img.Config.Env)
		})
	}
}

func testTargetStageNameArg(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM alpine AS base
WORKDIR /out
RUN echo -n "value:$TARGETSTAGE" > /out/first
ARG TARGETSTAGE
RUN echo -n "value:$TARGETSTAGE" > /out/second

FROM scratch AS foo
COPY --from=base /out/ /

FROM scratch
COPY --from=base /out/ /
`,
		`
FROM nanoserver AS base
WORKDIR /out
RUN echo value:%TARGETSTAGE%> /out/first
ARG TARGETSTAGE
RUN echo value:%TARGETSTAGE%> /out/second

FROM nanoserver AS foo
COPY --from=base /out/ /

FROM nanoserver
COPY --from=base /out/ /
`,
	))

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	destDir := t.TempDir()

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
		FrontendAttrs: map[string]string{
			"target": "foo",
		},
		Exports: []client.ExportEntry{
			{
				Type:      client.ExporterLocal,
				OutputDir: destDir,
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "first"))
	require.NoError(t, err)
	valueStr := integration.UnixOrWindows("value:", "value:%TARGETSTAGE%\r\n")
	require.Equal(t, valueStr, string(dt))

	dt, err = os.ReadFile(filepath.Join(destDir, "second"))
	require.NoError(t, err)
	lineEnd := integration.UnixOrWindows("", "\r\n")
	require.Equal(t, fmt.Sprintf("value:foo%s", lineEnd), string(dt))

	destDir = t.TempDir()

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
	require.NoError(t, err)

	dt, err = os.ReadFile(filepath.Join(destDir, "first"))
	require.NoError(t, err)
	require.Equal(t, valueStr, string(dt))

	dt, err = os.ReadFile(filepath.Join(destDir, "second"))
	require.NoError(t, err)
	require.Equal(t, fmt.Sprintf("value:default%s", lineEnd), string(dt))

	// stage name defined in Dockerfile but not passed in request
	imgName := integration.UnixOrWindows("scratch", "nanoserver")
	dockerfile = append(dockerfile, fmt.Appendf(nil, `
	
	FROM %s AS final
	COPY --from=base /out/ /
	`, imgName)...)

	dir = integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	destDir = t.TempDir()

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
	require.NoError(t, err)

	dt, err = os.ReadFile(filepath.Join(destDir, "first"))
	require.NoError(t, err)
	require.Equal(t, valueStr, string(dt))

	dt, err = os.ReadFile(filepath.Join(destDir, "second"))
	require.NoError(t, err)
	require.Equal(t, fmt.Sprintf("value:final%s", lineEnd), string(dt))
}

func testDefaultPathEnvOnWindows(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "!windows")

	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM nanoserver
USER ContainerAdministrator
RUN echo %PATH% > env_path.txt
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

	dt, err := os.ReadFile(filepath.Join(destDir, "env_path.txt"))
	require.NoError(t, err)

	envPath := string(dt)
	require.Contains(t, envPath, "C:\\Windows")
}

func testExportMultiPlatform(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	workers.CheckFeatureCompat(t, sb, workers.FeatureOCIExporter, workers.FeatureMultiPlatform)
	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM scratch
ARG TARGETARCH
ARG TARGETPLATFORM
LABEL target=$TARGETPLATFORM
COPY arch-$TARGETARCH whoami
`)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile("arch-arm", []byte(`i am arm`), 0600),
		fstest.CreateFile("arch-amd64", []byte(`i am amd64`), 0600),
		fstest.CreateFile("arch-s390x", []byte(`i am s390x`), 0600),
		fstest.CreateFile("arch-ppc64le", []byte(`i am ppc64le`), 0600),
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
		FrontendAttrs: map[string]string{
			"platform": "windows/amd64,linux/arm,linux/s390x",
		},
		Exports: []client.ExportEntry{
			{
				Type:      client.ExporterLocal,
				OutputDir: destDir,
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "windows_amd64/whoami"))
	require.NoError(t, err)
	require.Equal(t, "i am amd64", string(dt))

	dt, err = os.ReadFile(filepath.Join(destDir, "linux_arm_v7/whoami"))
	require.NoError(t, err)
	require.Equal(t, "i am arm", string(dt))

	dt, err = os.ReadFile(filepath.Join(destDir, "linux_s390x/whoami"))
	require.NoError(t, err)
	require.Equal(t, "i am s390x", string(dt))

	// repeat with oci exporter

	destDir = t.TempDir()

	out := filepath.Join(destDir, "out.tar")
	outW, err := os.Create(out)
	require.NoError(t, err)

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
		FrontendAttrs: map[string]string{
			"platform": "windows/amd64,linux/arm/v6,linux/ppc64le",
		},
		Exports: []client.ExportEntry{
			{
				Type:   client.ExporterOCI,
				Output: fixedWriteCloser(outW),
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err = os.ReadFile(filepath.Join(destDir, "out.tar"))
	require.NoError(t, err)

	m, err := testutil.ReadTarToMap(dt, false)
	require.NoError(t, err)

	var idx ocispecs.Index
	err = json.Unmarshal(m[ocispecs.ImageIndexFile].Data, &idx)
	require.NoError(t, err)

	mlistHex := idx.Manifests[0].Digest.Hex()

	idx = ocispecs.Index{}
	err = json.Unmarshal(m[ocispecs.ImageBlobsDir+"/sha256/"+mlistHex].Data, &idx)
	require.NoError(t, err)

	require.Equal(t, 3, len(idx.Manifests))

	for i, exp := range []struct {
		p    string
		os   string
		arch string
		dt   string
	}{
		{p: "windows/amd64", os: "windows", arch: "amd64", dt: "i am amd64"},
		{p: "linux/arm/v6", os: "linux", arch: "arm", dt: "i am arm"},
		{p: "linux/ppc64le", os: "linux", arch: "ppc64le", dt: "i am ppc64le"},
	} {
		t.Run(exp.p, func(t *testing.T) {
			require.Equal(t, exp.p, platforms.Format(*idx.Manifests[i].Platform))

			var mfst ocispecs.Manifest
			err = json.Unmarshal(m[ocispecs.ImageBlobsDir+"/sha256/"+idx.Manifests[i].Digest.Hex()].Data, &mfst)
			require.NoError(t, err)

			require.Equal(t, 1, len(mfst.Layers))

			m2, err := testutil.ReadTarToMap(m[ocispecs.ImageBlobsDir+"/sha256/"+mfst.Layers[0].Digest.Hex()].Data, true)
			require.NoError(t, err)
			require.Equal(t, exp.dt, string(m2["whoami"].Data))

			var img ocispecs.Image
			err = json.Unmarshal(m[ocispecs.ImageBlobsDir+"/sha256/"+mfst.Config.Digest.Hex()].Data, &img)
			require.NoError(t, err)

			require.Equal(t, exp.os, img.OS)
			require.Equal(t, exp.arch, img.Architecture)
			v, ok := img.Config.Labels["target"]
			require.True(t, ok)
			require.Equal(t, exp.p, v)
		})
	}
}

// tonistiigi/fsutil#46
func testContextChangeDirToFile(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM scratch
COPY foo /
`,
		`
FROM nanoserver
COPY foo /
`,
	))

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateDir("foo", 0700),
		fstest.CreateFile("foo/bar", []byte(`contents`), 0600),
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

	dir = integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile("foo", []byte(`contents2`), 0600),
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
	require.Equal(t, "contents2", string(dt))
}

func testNoSnapshotLeak(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM scratch
COPY foo /
`,
		`
FROM nanoserver
COPY foo /
`,
	))

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile("foo", []byte(`contents`), 0600),
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

	du, err := c.DiskUsage(sb.Context())
	require.NoError(t, err)

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)

	du2, err := c.DiskUsage(sb.Context())
	require.NoError(t, err)

	require.Equal(t, len(du), len(du2))
}

func testHTTPDockerfile(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM busybox
RUN echo -n "foo-contents" > /foo
FROM scratch
COPY --from=0 /foo /foo
`,
		`
FROM nanoserver
USER ContainerAdministrator
RUN echo foo-contents> /foo
`,
	))

	srcDir := t.TempDir()

	err := os.WriteFile(filepath.Join(srcDir, "Dockerfile"), dockerfile, 0600)
	require.NoError(t, err)

	resp := httpserver.Response{
		Etag:    identity.NewID(),
		Content: dockerfile,
	}

	server := httpserver.NewTestServer(map[string]httpserver.Response{
		"/df": resp,
	})
	defer server.Close()

	destDir := t.TempDir()

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"context":  server.URL + "/df",
			"filename": "mydockerfile", // this is bogus, any name should work
		},
		Exports: []client.ExportEntry{
			{
				Type:      client.ExporterLocal,
				OutputDir: destDir,
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "foo"))
	require.NoError(t, err)
	foo := integration.UnixOrWindows(
		"foo-contents",
		"foo-contents\r\n", // Windows echo command adds \r\n
	)
	require.Equal(t, foo, string(dt))
}

func testCmdShell(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	cdAddress := sb.ContainerdAddress()
	if cdAddress == "" {
		t.Skip("test is only for containerd worker")
	}

	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM scratch
CMD ["test"]
`,
		`
FROM nanoserver
CMD ["test"]
`,
	))

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	target := "docker.io/moby/cmdoverridetest:latest"
	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		Exports: []client.ExportEntry{
			{
				Type: client.ExporterImage,
				Attrs: map[string]string{
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
SHELL ["ls"]
ENTRYPOINT my entrypoint
`, target)

	dir = integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	target = "docker.io/moby/cmdoverridetest2:latest"
	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		Exports: []client.ExportEntry{
			{
				Type: client.ExporterImage,
				Attrs: map[string]string{
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

	ctr, err := newContainerd(cdAddress)
	require.NoError(t, err)
	defer ctr.Close()

	ctx := namespaces.WithNamespace(sb.Context(), "buildkit")

	img, err := ctr.ImageService().Get(ctx, target)
	require.NoError(t, err)

	desc, err := img.Config(ctx, ctr.ContentStore(), platforms.Default())
	require.NoError(t, err)

	dt, err := content.ReadBlob(ctx, ctr.ContentStore(), desc)
	require.NoError(t, err)

	var ociimg ocispecs.Image
	err = json.Unmarshal(dt, &ociimg)
	require.NoError(t, err)

	require.Equal(t, []string(nil), ociimg.Config.Cmd)
	require.Equal(t, []string{"ls", "my entrypoint"}, ociimg.Config.Entrypoint)
}

func testInvalidJSONCommands(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM alpine
RUN ["echo", "hello"]this is invalid
`,
		`
FROM nanoserver
RUN ["echo", "hello"]this is invalid
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
	require.Error(t, err)
	require.Contains(t, err.Error(), "this is invalid")

	workers.CheckFeatureCompat(t, sb,
		workers.FeatureDirectPush,
	)

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	target := registry + "/buildkit/testexportdf:multi"

	dockerfile = []byte(integration.UnixOrWindows(
		`
FROM alpine
ENTRYPOINT []random string
`,
		`
FROM nanoserver
ENTRYPOINT []random string
`,
	))

	dir = integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		Exports: []client.ExportEntry{
			{
				Type: client.ExporterImage,
				Attrs: map[string]string{
					"name": target,
					"push": "true",
				},
			},
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)

	desc, provider, err := contentutil.ProviderFromRef(target)
	require.NoError(t, err)

	imgs, err := testutil.ReadImages(sb.Context(), provider, desc)
	require.NoError(t, err)

	require.Len(t, imgs.Images, 1)
	img := imgs.Images[0].Img

	entrypoint := integration.UnixOrWindows(
		[]string{"/bin/sh", "-c", "[]random string"},
		[]string{"cmd", "/S", "/C", "[]random string"},
	)
	require.Equal(t, entrypoint, img.Config.Entrypoint)
}

func testPullScratch(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	f := getFrontend(t, sb)

	cdAddress := sb.ContainerdAddress()
	if cdAddress == "" {
		t.Skip("test is only for containerd worker")
	}

	dockerfile := []byte(`
FROM scratch
LABEL foo=bar
`)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	target := "docker.io/moby/testpullscratch:latest"
	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		Exports: []client.ExportEntry{
			{
				Type: client.ExporterImage,
				Attrs: map[string]string{
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

	dockerfile = []byte(`
FROM docker.io/moby/testpullscratch:latest
LABEL bar=baz
COPY foo .
`)

	dir = integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile("foo", []byte("foo-contents"), 0600),
	)

	target = "docker.io/moby/testpullscratch2:latest"
	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		Exports: []client.ExportEntry{
			{
				Type: client.ExporterImage,
				Attrs: map[string]string{
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

	ctr, err := newContainerd(cdAddress)
	require.NoError(t, err)
	defer ctr.Close()

	ctx := namespaces.WithNamespace(sb.Context(), "buildkit")

	img, err := ctr.ImageService().Get(ctx, target)
	require.NoError(t, err)

	desc, err := img.Config(ctx, ctr.ContentStore(), platforms.Default())
	require.NoError(t, err)

	dt, err := content.ReadBlob(ctx, ctr.ContentStore(), desc)
	require.NoError(t, err)

	var ociimg ocispecs.Image
	err = json.Unmarshal(dt, &ociimg)
	require.NoError(t, err)

	require.Equal(t, "layers", ociimg.RootFS.Type)
	require.Equal(t, 1, len(ociimg.RootFS.DiffIDs))
	v, ok := ociimg.Config.Labels["foo"]
	require.True(t, ok)
	require.Equal(t, "bar", v)
	v, ok = ociimg.Config.Labels["bar"]
	require.True(t, ok)
	require.Equal(t, "baz", v)

	echo := llb.Image("busybox").
		Run(llb.Shlex(`sh -c "echo -n foo0 > /empty/foo"`)).
		AddMount("/empty", llb.Image("docker.io/moby/testpullscratch:latest"))

	def, err := echo.Marshal(sb.Context())
	require.NoError(t, err)

	destDir := t.TempDir()

	_, err = c.Solve(sb.Context(), def, client.SolveOpt{
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

	dt, err = os.ReadFile(filepath.Join(destDir, "foo"))
	require.NoError(t, err)
	require.Equal(t, "foo0", string(dt))
}

func testGlobalArg(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(integration.UnixOrWindows(
		`
ARG tag=nosuchtag
FROM busybox:${tag}
`,
		`
ARG tag=nosuchtag
FROM nanoserver:${tag}
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
		FrontendAttrs: map[string]string{
			"build-arg:tag": "latest",
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)
}

func testDirs(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)
	f.RequiresBuildctl(t)

	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM busybox
COPY foo /foo2
COPY foo /
RUN echo -n bar > foo3
RUN test -f foo
RUN cmp -s foo foo2
RUN cmp -s foo foo3
`,
		`
FROM nanoserver:plus
USER ContainerAdministrator
COPY foo /foo2
COPY foo /
RUN echo bar> foo3
RUN IF EXIST foo (exit 0) ELSE (exit 1)
RUN fc /b foo foo2 && (exit 0) || (exit 1)
RUN fc /b foo foo3 && (exit 0) || (exit 1)
`,
	))

	bar := integration.UnixOrWindows(`bar`, "bar\r\n")

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile("foo", []byte(bar), 0600),
	)

	args, trace := f.DFCmdArgs(dir.Name, dir.Name)
	defer os.RemoveAll(trace)

	cmd := sb.Cmd(args)
	stdout := new(bytes.Buffer)
	cmd.Stderr = stdout
	err1 := cmd.Run()
	require.NoError(t, err1)

	_, err := os.Stat(trace)
	require.NoError(t, err)

	// relative urls
	args, trace = f.DFCmdArgs(".", ".")
	defer os.RemoveAll(trace)

	cmd = sb.Cmd(args)
	cmd.Dir = dir.Name
	stdout.Reset()
	cmd.Stderr = stdout
	err2 := cmd.Run()
	require.NoError(t, err2)

	_, err = os.Stat(trace)
	require.NoError(t, err)

	// different context and dockerfile directories
	dir1 := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	dir2 := integration.Tmpdir(
		t,
		fstest.CreateFile("foo", []byte(bar), 0600),
	)

	args, trace = f.DFCmdArgs(dir2.Name, dir1.Name)
	defer os.RemoveAll(trace)

	cmd = sb.Cmd(args)
	cmd.Dir = dir.Name
	require.NoError(t, cmd.Run())

	_, err = os.Stat(trace)
	require.NoError(t, err)

	// TODO: test trace file output, cache hits, logs etc.
	// TODO: output metadata about original dockerfile command in trace
}

func testInvalidCommand(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)
	f.RequiresBuildctl(t)
	dockerfile := []byte(integration.UnixOrWindows(
		`
	FROM busybox
	RUN invalidcmd
`,
		`
	FROM nanoserver
	RUN invalidcmd
`,
	))

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	args, trace := f.DFCmdArgs(dir.Name, dir.Name)
	defer os.RemoveAll(trace)

	cmd := sb.Cmd(args)
	stdout := new(bytes.Buffer)
	cmd.Stderr = stdout
	err := cmd.Run()
	require.Error(t, err)
	require.Contains(t, stdout.String(), integration.UnixOrWindows(
		"/bin/sh -c invalidcmd",
		"cmd /S /C invalidcmd",
	))
	require.Contains(t, stdout.String(), integration.UnixOrWindows(
		"did not complete successfully",
		"'invalidcmd' is not recognized as an internal or external command",
	))
}

func testDockerfileInvalidInstruction(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)
	f.RequiresBuildctl(t)
	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM scratch
FNTRYPOINT ["/bin/sh", "-c", "echo invalidinstruction"]
`,
		`
FROM nanoserver
FNTRYPOINT ["cmd", "/c", "echo invalidinstruction"]
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

	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown instruction: FNTRYPOINT")
	require.Contains(t, err.Error(), "did you mean ENTRYPOINT?")
}

func testSymlinkDestination(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	f := getFrontend(t, sb)
	f.RequiresBuildctl(t)

	buf := bytes.NewBuffer(nil)
	tw := tar.NewWriter(buf)
	expectedContent := []byte("content0")
	err := tw.WriteHeader(&tar.Header{
		Name:     "symlink",
		Typeflag: tar.TypeSymlink,
		Linkname: "../tmp/symlink-target",
		Mode:     0755,
	})
	require.NoError(t, err)
	err = tw.Close()
	require.NoError(t, err)

	dockerfile := []byte(`
FROM scratch
ADD t.tar /
COPY foo /symlink/
`)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile("foo", expectedContent, 0600),
		fstest.CreateFile("t.tar", buf.Bytes(), 0600),
	)

	args, trace := f.DFCmdArgs(dir.Name, dir.Name)
	defer os.RemoveAll(trace)

	destDir := t.TempDir()

	cmd := sb.Cmd(args + fmt.Sprintf(" --output type=local,dest=%s", destDir))
	require.NoError(t, cmd.Run())

	dt, err := os.ReadFile(filepath.Join(destDir, "tmp/symlink-target/foo"))
	require.NoError(t, err)
	require.Equal(t, expectedContent, dt)
}

func testScratchConfig(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	cdAddress := sb.ContainerdAddress()
	if cdAddress == "" {
		t.Skip("test requires containerd worker")
	}

	f := getFrontend(t, sb)
	f.RequiresBuildctl(t)
	dockerfile := []byte(`
FROM scratch
ENV foo=bar
`)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	args, trace := f.DFCmdArgs(dir.Name, dir.Name)
	defer os.RemoveAll(trace)

	target := "example.com/moby/dockerfilescratch:test"
	cmd := sb.Cmd(args + " --output type=image,name=" + target)
	err := cmd.Run()
	require.NoError(t, err)

	client, err := newContainerd(cdAddress)
	require.NoError(t, err)
	defer client.Close()

	ctx := namespaces.WithNamespace(sb.Context(), "buildkit")

	img, err := client.ImageService().Get(ctx, target)
	require.NoError(t, err)

	desc, err := img.Config(ctx, client.ContentStore(), platforms.Default())
	require.NoError(t, err)

	dt, err := content.ReadBlob(ctx, client.ContentStore(), desc)
	require.NoError(t, err)

	var ociimg ocispecs.Image
	err = json.Unmarshal(dt, &ociimg)
	require.NoError(t, err)

	require.NotEqual(t, "", ociimg.OS)
	require.NotEqual(t, "", ociimg.Architecture)
	require.NotEqual(t, "", ociimg.Config.WorkingDir)
	require.Equal(t, "layers", ociimg.RootFS.Type)
	require.Equal(t, 0, len(ociimg.RootFS.DiffIDs))

	require.Equal(t, 1, len(ociimg.History))
	require.Contains(t, ociimg.History[0].CreatedBy, "ENV foo=bar")
	require.Equal(t, true, ociimg.History[0].EmptyLayer)

	require.Contains(t, ociimg.Config.Env, "foo=bar")
	require.Condition(t, func() bool {
		for _, env := range ociimg.Config.Env {
			if strings.HasPrefix(env, "PATH=") {
				return true
			}
		}
		return false
	})
}

func testExposeExpansion(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureImageExporter)
	f := getFrontend(t, sb)

	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM scratch
ARG PORTS="3000 4000/udp"
EXPOSE $PORTS
EXPOSE 5000
`,
		`
FROM nanoserver
ARG PORTS="3000 4000/udp"
EXPOSE $PORTS
EXPOSE 5000
`,
	))

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	target := "example.com/moby/dockerfileexpansion:test"
	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		Exports: []client.ExportEntry{
			{
				Type: client.ExporterImage,
				Attrs: map[string]string{
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

	cdAddress := sb.ContainerdAddress()
	if cdAddress == "" {
		t.Skip("rest of test requires containerd worker")
	}

	client, err := newContainerd(cdAddress)
	require.NoError(t, err)
	defer client.Close()

	ctx := namespaces.WithNamespace(sb.Context(), "buildkit")

	img, err := client.ImageService().Get(ctx, target)
	require.NoError(t, err)

	desc, err := img.Config(ctx, client.ContentStore(), platforms.Default())
	require.NoError(t, err)

	dt, err := content.ReadBlob(ctx, client.ContentStore(), desc)
	require.NoError(t, err)

	var ociimg ocispecs.Image
	err = json.Unmarshal(dt, &ociimg)
	require.NoError(t, err)

	require.Equal(t, 3, len(ociimg.Config.ExposedPorts))

	var ports []string
	for p := range ociimg.Config.ExposedPorts {
		ports = append(ports, p)
	}
	slices.Sort(ports)

	require.Equal(t, "3000/tcp", ports[0])
	require.Equal(t, "4000/udp", ports[1])
	require.Equal(t, "5000/tcp", ports[2])
}

func testDockerignore(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM scratch
COPY . .
`,
		`
FROM nanoserver
COPY . .
`,
	))

	dockerignore := []byte(`
ba*
Dockerfile
!bay
.dockerignore
`)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile("foo", []byte(`foo-contents`), 0600),
		fstest.CreateFile("bar", []byte(`bar-contents`), 0600),
		fstest.CreateFile("baz", []byte(`baz-contents`), 0600),
		fstest.CreateFile("bay", []byte(`bay-contents`), 0600),
		fstest.CreateFile(".dockerignore", dockerignore, 0600),
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

	dt, err := os.ReadFile(filepath.Join(destDir, "foo"))
	require.NoError(t, err)
	require.Equal(t, "foo-contents", string(dt))

	_, err = os.Stat(filepath.Join(destDir, ".dockerignore"))
	require.Error(t, err)
	require.True(t, errors.Is(err, os.ErrNotExist))

	_, err = os.Stat(filepath.Join(destDir, "Dockerfile"))
	require.Error(t, err)
	require.True(t, errors.Is(err, os.ErrNotExist))

	_, err = os.Stat(filepath.Join(destDir, "bar"))
	require.Error(t, err)
	require.True(t, errors.Is(err, os.ErrNotExist))

	_, err = os.Stat(filepath.Join(destDir, "baz"))
	require.Error(t, err)
	require.True(t, errors.Is(err, os.ErrNotExist))

	dt, err = os.ReadFile(filepath.Join(destDir, "bay"))
	require.NoError(t, err)
	require.Equal(t, "bay-contents", string(dt))
}

func testDockerignoreInvalid(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM scratch
COPY . .
`,
		`
FROM nanoserver
COPY . .
`,
	))

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile(".dockerignore", []byte("!\n"), 0600),
	)

	ctx, cancel := context.WithCancelCause(sb.Context())
	ctx, _ = context.WithTimeoutCause(ctx, 15*time.Second, errors.WithStack(context.DeadlineExceeded)) //nolint:govet
	defer func() { cancel(errors.WithStack(context.Canceled)) }()

	c, err := client.New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	_, err = f.Solve(ctx, c, client.SolveOpt{
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	// err is either the expected error due to invalid dockerignore or error from the timeout
	require.Error(t, err)
	select {
	case <-ctx.Done():
		t.Fatal("timed out")
	default:
	}
}

// moby/moby#10858
func testDockerfileLowercase(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM scratch
`, `
FROM nanoserver
`,
	))

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("dockerfile", dockerfile, 0600),
	)

	ctx := sb.Context()

	c, err := client.New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	_, err = f.Solve(ctx, c, client.SolveOpt{
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)
}

func testDockerfileFromGit(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	f := getFrontend(t, sb)

	gitDir := t.TempDir()

	dockerfile := `
FROM busybox AS build
RUN echo -n fromgit > foo
FROM scratch
COPY --from=build foo bar
`

	err := os.WriteFile(filepath.Join(gitDir, "Dockerfile"), []byte(dockerfile), 0600)
	require.NoError(t, err)

	err = runShell(gitDir,
		"git init",
		"git config --local user.email test",
		"git config --local user.name test",
		"git add Dockerfile",
		"git commit -m initial",
		"git branch first",
	)
	require.NoError(t, err)

	dockerfile += `
COPY --from=build foo bar2
`

	err = os.WriteFile(filepath.Join(gitDir, "Dockerfile"), []byte(dockerfile), 0600)
	require.NoError(t, err)

	err = runShell(gitDir,
		"git add Dockerfile",
		"git commit -m second",
		"git update-server-info",
	)
	require.NoError(t, err)

	server := httptest.NewServer(http.FileServer(http.Dir(filepath.Clean(gitDir))))
	defer server.Close()

	destDir := t.TempDir()

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"context": server.URL + "/.git#first",
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
	require.Equal(t, "fromgit", string(dt))

	_, err = os.Stat(filepath.Join(destDir, "bar2"))
	require.Error(t, err)
	require.True(t, errors.Is(err, os.ErrNotExist))

	// second request from master branch contains both files
	destDir = t.TempDir()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"context": server.URL + "/.git",
		},
		Exports: []client.ExportEntry{
			{
				Type:      client.ExporterLocal,
				OutputDir: destDir,
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err = os.ReadFile(filepath.Join(destDir, "bar"))
	require.NoError(t, err)
	require.Equal(t, "fromgit", string(dt))

	dt, err = os.ReadFile(filepath.Join(destDir, "bar2"))
	require.NoError(t, err)
	require.Equal(t, "fromgit", string(dt))
}

func testDockerfileFromHTTP(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	buf := bytes.NewBuffer(nil)
	w := tar.NewWriter(buf)

	writeFile := func(fn, dt string) {
		err := w.WriteHeader(&tar.Header{
			Name:     fn,
			Mode:     0600,
			Size:     int64(len(dt)),
			Typeflag: tar.TypeReg,
		})
		require.NoError(t, err)
		_, err = w.Write([]byte(dt))
		require.NoError(t, err)
	}

	dockerfile := fmt.Sprintf(`FROM %s
COPY foo bar
`, integration.UnixOrWindows("scratch", "nanoserver"))
	writeFile("mydockerfile", dockerfile)

	writeFile("foo", "foo-contents")

	require.NoError(t, w.Flush())

	resp := httpserver.Response{
		Etag:    identity.NewID(),
		Content: buf.Bytes(),
	}

	server := httpserver.NewTestServer(map[string]httpserver.Response{
		"/myurl": resp,
	})
	defer server.Close()

	destDir := t.TempDir()

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"context":  server.URL + "/myurl",
			"filename": "mydockerfile",
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
	require.Equal(t, "foo-contents", string(dt))
}

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

func testLabels(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureImageExporter)
	f := getFrontend(t, sb)

	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM scratch
LABEL foo=bar
`,
		`
FROM nanoserver
LABEL foo=bar
`,
	))

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	target := "example.com/moby/dockerfilelabels:test"
	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"label:bar": "baz",
		},
		Exports: []client.ExportEntry{
			{
				Type: client.ExporterImage,
				Attrs: map[string]string{
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

	cdAddress := sb.ContainerdAddress()
	if cdAddress == "" {
		t.Skip("rest of test requires containerd worker")
	}

	client, err := newContainerd(cdAddress)
	require.NoError(t, err)
	defer client.Close()

	ctx := namespaces.WithNamespace(sb.Context(), "buildkit")

	img, err := client.ImageService().Get(ctx, target)
	require.NoError(t, err)

	desc, err := img.Config(ctx, client.ContentStore(), platforms.Default())
	require.NoError(t, err)

	dt, err := content.ReadBlob(ctx, client.ContentStore(), desc)
	require.NoError(t, err)

	var ociimg ocispecs.Image
	err = json.Unmarshal(dt, &ociimg)
	require.NoError(t, err)

	v, ok := ociimg.Config.Labels["foo"]
	require.True(t, ok)
	require.Equal(t, "bar", v)

	v, ok = ociimg.Config.Labels["bar"]
	require.True(t, ok)
	require.Equal(t, "baz", v)
}

func testReproducibleIDs(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureImageExporter)
	f := getFrontend(t, sb)

	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM busybox
ENV foo=bar
COPY foo /
RUN echo bar > bar
`,
		`
FROM nanoserver
USER ContainerAdministrator
ENV foo=bar
COPY foo /
RUN echo bar > bar
`,
	))
	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile("foo", []byte("foo-contents"), 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	target := "example.com/moby/dockerfileids:test"
	opt := client.SolveOpt{
		FrontendAttrs: map[string]string{},
		Exports: []client.ExportEntry{
			{
				Type: client.ExporterImage,
				Attrs: map[string]string{
					"name": target,
				},
			},
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}

	_, err = f.Solve(sb.Context(), c, opt, nil)
	require.NoError(t, err)

	target2 := "example.com/moby/dockerfileids2:test"
	opt.Exports[0].Attrs["name"] = target2

	_, err = f.Solve(sb.Context(), c, opt, nil)
	require.NoError(t, err)

	cdAddress := sb.ContainerdAddress()
	if cdAddress == "" {
		t.Skip("rest of test requires containerd worker")
	}

	client, err := newContainerd(cdAddress)
	require.NoError(t, err)
	defer client.Close()

	ctx := namespaces.WithNamespace(sb.Context(), "buildkit")

	img, err := client.ImageService().Get(ctx, target)
	require.NoError(t, err)
	img2, err := client.ImageService().Get(ctx, target2)
	require.NoError(t, err)

	require.Equal(t, img.Target, img2.Target)
}

func testPlatformArgsImplicit(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfileStr := integration.UnixOrWindows(
		`
FROM scratch AS build-%s
COPY foo bar
FROM build-${TARGETOS}
COPY foo2 bar2
`,
		`
FROM nanoserver AS build-%s
COPY foo bar
FROM build-${TARGETOS}
COPY foo2 bar2
`,
	)

	dockerfile := fmt.Appendf(nil, dockerfileStr, runtime.GOOS)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile("foo", []byte("d0"), 0600),
		fstest.CreateFile("foo2", []byte("d1"), 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	destDir := t.TempDir()

	opt := client.SolveOpt{
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
	}

	_, err = f.Solve(sb.Context(), c, opt, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "bar"))
	require.NoError(t, err)
	require.Equal(t, "d0", string(dt))

	dt, err = os.ReadFile(filepath.Join(destDir, "bar2"))
	require.NoError(t, err)
	require.Equal(t, "d1", string(dt))
}

func testPlatformArgsExplicit(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM --platform=$BUILDPLATFORM busybox AS build
ARG TARGETPLATFORM
ARG TARGETOS
RUN mkdir /out && echo -n $TARGETPLATFORM > /out/platform && echo -n $TARGETOS > /out/os
FROM scratch
COPY --from=build out .
`)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	destDir := t.TempDir()

	opt := client.SolveOpt{
		Exports: []client.ExportEntry{
			{
				Type:      client.ExporterLocal,
				OutputDir: destDir,
			},
		},
		FrontendAttrs: map[string]string{
			"platform":           "darwin/ppc64le",
			"build-arg:TARGETOS": "freebsd",
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}

	_, err = f.Solve(sb.Context(), c, opt, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "platform"))
	require.NoError(t, err)
	require.Equal(t, "darwin/ppc64le", string(dt))

	dt, err = os.ReadFile(filepath.Join(destDir, "os"))
	require.NoError(t, err)
	require.Equal(t, "freebsd", string(dt))
}

func testBuiltinArgs(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM busybox AS build
ARG FOO
ARG BAR
ARG BAZ=bazcontent
RUN echo -n $HTTP_PROXY::$NO_PROXY::$FOO::$BAR::$BAZ > /out
FROM scratch
COPY --from=build /out /

`, `
FROM nanoserver AS build
USER ContainerAdministrator
ARG FOO
ARG BAR
ARG BAZ=bazcontent
RUN echo %HTTP_PROXY%::%NO_PROXY%::%FOO%::%BAR%::%BAZ%> out
FROM nanoserver
COPY --from=build out /
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

	opt := client.SolveOpt{
		FrontendAttrs: map[string]string{
			"build-arg:FOO":        "foocontents",
			"build-arg:http_proxy": "hpvalue",
			"build-arg:NO_PROXY":   "npvalue",
		},
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
	}

	_, err = f.Solve(sb.Context(), c, opt, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "out"))
	require.NoError(t, err)
	// Windows can't interpret empty env variables, %BAR% handles empty values.
	expectedStr := integration.UnixOrWindows(`hpvalue::npvalue::foocontents::::bazcontent`, "hpvalue::npvalue::foocontents::%BAR%::bazcontent\r\n")
	require.Equal(t, expectedStr, string(dt))

	// repeat with changed default args should match the old cache
	destDir = t.TempDir()

	opt = client.SolveOpt{
		FrontendAttrs: map[string]string{
			"build-arg:FOO":        "foocontents",
			"build-arg:http_proxy": "hpvalue2",
		},
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
	}

	_, err = f.Solve(sb.Context(), c, opt, nil)
	require.NoError(t, err)

	dt, err = os.ReadFile(filepath.Join(destDir, "out"))
	require.NoError(t, err)
	expectedStr = integration.UnixOrWindows("hpvalue::npvalue::foocontents::::bazcontent", "hpvalue::npvalue::foocontents::%BAR%::bazcontent\r\n")
	require.Equal(t, expectedStr, string(dt))

	// changing actual value invalidates cache
	destDir = t.TempDir()

	opt = client.SolveOpt{
		FrontendAttrs: map[string]string{
			"build-arg:FOO":        "foocontents2",
			"build-arg:http_proxy": "hpvalue2",
		},
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
	}

	_, err = f.Solve(sb.Context(), c, opt, nil)
	require.NoError(t, err)

	dt, err = os.ReadFile(filepath.Join(destDir, "out"))
	require.NoError(t, err)
	expectedStr = integration.UnixOrWindows("hpvalue2::::foocontents2::::bazcontent", "hpvalue2::%NO_PROXY%::foocontents2::%BAR%::bazcontent\r\n")
	require.Equal(t, expectedStr, string(dt))
}

func testTarContext(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	imgName := integration.UnixOrWindows("scratch", "nanoserver")
	dockerfile := fmt.Appendf(nil, `
FROM %s
COPY foo /`, imgName)

	foo := []byte("contents")

	buf := bytes.NewBuffer(nil)
	tw := tar.NewWriter(buf)
	err := tw.WriteHeader(&tar.Header{
		Name:     "Dockerfile",
		Typeflag: tar.TypeReg,
		Size:     int64(len(dockerfile)),
		Mode:     0644,
	})
	require.NoError(t, err)
	_, err = tw.Write(dockerfile)
	require.NoError(t, err)
	err = tw.WriteHeader(&tar.Header{
		Name:     "foo",
		Typeflag: tar.TypeReg,
		Size:     int64(len(foo)),
		Mode:     0644,
	})
	require.NoError(t, err)
	_, err = tw.Write(foo)
	require.NoError(t, err)
	err = tw.Close()
	require.NoError(t, err)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	up := uploadprovider.New()
	url := up.Add(io.NopCloser(buf))

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"context": url,
		},
		Session: []session.Attachable{up},
	}, nil)
	require.NoError(t, err)
}

func testTarContextExternalDockerfile(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	foo := []byte("contents")

	buf := bytes.NewBuffer(nil)
	tw := tar.NewWriter(buf)
	err := tw.WriteHeader(&tar.Header{
		Name:     "sub/dir/foo",
		Typeflag: tar.TypeReg,
		Size:     int64(len(foo)),
		Mode:     0644,
	})
	require.NoError(t, err)
	_, err = tw.Write(foo)
	require.NoError(t, err)
	err = tw.Close()
	require.NoError(t, err)

	imgName := integration.UnixOrWindows("scratch", "nanoserver")
	dockerfile := fmt.Appendf(nil, `
FROM %s
COPY foo bar
`, imgName)
	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	up := uploadprovider.New()
	url := up.Add(io.NopCloser(buf))

	// repeat with changed default args should match the old cache
	destDir := t.TempDir()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"context":       url,
			"dockerfilekey": dockerui.DefaultLocalNameDockerfile,
			"contextsubdir": "sub/dir",
		},
		Session: []session.Attachable{up},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
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
	require.Equal(t, "contents", string(dt))
}

func testFrontendUseForwardedSolveResults(t *testing.T, sb integration.Sandbox) {
	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	dockerfileStr := `
FROM %s
COPY foo foo2
`
	dockerfile := fmt.Appendf(nil, dockerfileStr, integration.UnixOrWindows("scratch", "nanoserver"))
	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile("foo", []byte("data"), 0600),
	)

	frontend := func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		res, err := c.Solve(ctx, gateway.SolveRequest{
			Frontend: "dockerfile.v0",
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

		st := llb.Scratch().File(
			llb.Copy(st2, "foo2", "foo3"),
		)

		def, err := st.Marshal(sb.Context())
		if err != nil {
			return nil, err
		}

		return c.Solve(ctx, gateway.SolveRequest{
			Definition: def.ToPB(),
		})
	}

	destDir := t.TempDir()

	_, err = c.Build(sb.Context(), client.SolveOpt{
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
	}, "", frontend, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "foo3"))
	require.NoError(t, err)
	require.Equal(t, dt, []byte("data"))
}

func testFrontendEvaluate(t *testing.T, sb integration.Sandbox) {
	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM scratch
COPY badfile /
`,
		`
FROM nanoserver
COPY badfile /
`,
	))
	dir := integration.Tmpdir(t, fstest.CreateFile("Dockerfile", dockerfile, 0600))

	frontend := func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		_, err := c.Solve(ctx, gateway.SolveRequest{
			Frontend: "dockerfile.v0",
			Evaluate: true,
		})
		require.ErrorContains(t, err, `"/badfile": not found`)

		platformOpt := integration.UnixOrWindows("linux/amd64,linux/arm64", "windows/amd64")
		_, err = c.Solve(ctx, gateway.SolveRequest{
			Frontend: "dockerfile.v0",
			FrontendOpt: map[string]string{
				"platform": platformOpt,
			},
			Evaluate: true,
		})
		require.ErrorContains(t, err, `"/badfile": not found`)

		return nil, nil
	}

	_, err = c.Build(sb.Context(), client.SolveOpt{
		Exports: []client.ExportEntry{},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, "", frontend, nil)
	require.NoError(t, err)
}

func testFrontendInputs(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	f := getFrontend(t, sb)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	destDir := t.TempDir()

	outMount := llb.Image("busybox").Run(
		llb.Shlex(`sh -c "cat /dev/urandom | head -c 100 | sha256sum > /out/foo"`),
	).AddMount("/out", llb.Scratch())

	def, err := outMount.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, client.SolveOpt{
		Exports: []client.ExportEntry{
			{
				Type:      client.ExporterLocal,
				OutputDir: destDir,
			},
		},
	}, nil)
	require.NoError(t, err)

	expected, err := os.ReadFile(filepath.Join(destDir, "foo"))
	require.NoError(t, err)

	dockerfile := []byte(`
FROM scratch
COPY foo foo2
`)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		Exports: []client.ExportEntry{
			{
				Type:      client.ExporterLocal,
				OutputDir: destDir,
			},
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
		},
		FrontendInputs: map[string]llb.State{
			dockerui.DefaultLocalNameContext: outMount,
		},
	}, nil)
	require.NoError(t, err)

	actual, err := os.ReadFile(filepath.Join(destDir, "foo2"))
	require.NoError(t, err)
	require.Equal(t, expected, actual)
}

func testFrontendSubrequests(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)
	if _, ok := f.(*clientFrontend); !ok {
		t.Skip("only test with client frontend")
	}

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM scratch
COPY Dockerfile Dockerfile
`,
		`
FROM nanoserver
COPY Dockerfile Dockerfile
`,
	))

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	called := false

	frontend := func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		reqs, err := subrequests.Describe(ctx, c)
		require.NoError(t, err)

		require.Greater(t, len(reqs), 0)

		hasDescribe := false

		for _, req := range reqs {
			if req.Name == "frontend.subrequests.describe" {
				hasDescribe = true
				require.Equal(t, subrequests.RequestType("rpc"), req.Type)
				require.NotEqual(t, "", req.Version)
				require.Greater(t, len(req.Metadata), 0)
				require.Equal(t, "result.json", req.Metadata[0].Name)
			}
		}
		require.True(t, hasDescribe)

		_, err = c.Solve(ctx, gateway.SolveRequest{
			FrontendOpt: map[string]string{
				"requestid":     "frontend.subrequests.notexist",
				"frontend.caps": "moby.buildkit.frontend.subrequests",
			},
			Frontend: "dockerfile.v0",
		})
		require.Error(t, err)
		var reqErr *errdefs.UnsupportedSubrequestError
		require.ErrorAs(t, err, &reqErr)
		require.Equal(t, "frontend.subrequests.notexist", reqErr.GetName())

		_, err = c.Solve(ctx, gateway.SolveRequest{
			FrontendOpt: map[string]string{
				"frontend.caps": "moby.buildkit.frontend.notexistcap",
			},
			Frontend: "dockerfile.v0",
		})
		require.Error(t, err)
		var capErr *errdefs.UnsupportedFrontendCapError
		require.ErrorAs(t, err, &capErr)
		require.Equal(t, "moby.buildkit.frontend.notexistcap", capErr.GetName())

		called = true
		return nil, nil
	}

	_, err = c.Build(sb.Context(), client.SolveOpt{
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
		},
	}, "", frontend, nil)
	require.NoError(t, err)

	require.True(t, called)
}

// moby/buildkit#1301
func testDockerfileCheckHostname(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)
	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM busybox
RUN cat /etc/hosts | grep foo
RUN echo $HOSTNAME | grep foo
RUN echo $(hostname) | grep foo
`,
		`	
FROM nanoserver
RUN  reg query "HKEY_LOCAL_MACHINE\SYSTEM\CurrentControlSet\Services\Tcpip\Parameters" /v Hostname | findstr "foo"
`,
	))

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	cases := []struct {
		name  string
		attrs map[string]string
	}{
		{
			name: "meta",
			attrs: map[string]string{
				"hostname": "foo",
			},
		},
		{
			name: "arg",
			attrs: map[string]string{
				"build-arg:BUILDKIT_SANDBOX_HOSTNAME": "foo",
			},
		},
		{
			name: "meta and arg",
			attrs: map[string]string{
				"hostname":                            "bar",
				"build-arg:BUILDKIT_SANDBOX_HOSTNAME": "foo",
			},
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			_, err = f.Solve(sb.Context(), c, client.SolveOpt{
				FrontendAttrs: tt.attrs,
				LocalMounts: map[string]fsutil.FS{
					dockerui.DefaultLocalNameDockerfile: dir,
					dockerui.DefaultLocalNameContext:    dir,
				},
			}, nil)
			require.NoError(t, err)
		})
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

func testShmSize(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	f := getFrontend(t, sb)
	dockerfile := []byte(`
FROM busybox AS base
RUN mount | grep /dev/shm > /shmsize
FROM scratch
COPY --from=base /shmsize /
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
		FrontendAttrs: map[string]string{
			"shm-size": "134217728",
		},
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
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "shmsize"))
	require.NoError(t, err)
	require.Contains(t, string(dt), `size=131072k`)
}

func testUlimit(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	f := getFrontend(t, sb)
	dockerfile := []byte(`
FROM busybox AS base
RUN ulimit -n > /ulimit
FROM scratch
COPY --from=base /ulimit /
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
		FrontendAttrs: map[string]string{
			"ulimit": "nofile=1062:1062",
		},
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
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "ulimit"))
	require.NoError(t, err)
	require.Equal(t, `1062`, strings.TrimSpace(string(dt)))
}

func testCgroupParent(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	if sb.Rootless() {
		t.SkipNow()
	}

	if _, err := os.Lstat("/sys/fs/cgroup/cgroup.subtree_control"); os.IsNotExist(err) {
		t.Skipf("test requires cgroup v2")
	}

	cgroupName := "test." + identity.NewID()

	err := os.MkdirAll(filepath.Join("/sys/fs/cgroup", cgroupName), 0755)
	require.NoError(t, err)

	defer func() {
		err := os.RemoveAll(filepath.Join("/sys/fs/cgroup", cgroupName))
		require.NoError(t, err)
	}()

	err = os.WriteFile(filepath.Join("/sys/fs/cgroup", cgroupName, "pids.max"), []byte("10"), 0644)
	require.NoError(t, err)

	f := getFrontend(t, sb)
	dockerfile := []byte(`
FROM alpine AS base
RUN mkdir /out; (for i in $(seq 1 10); do sleep 1 & done 2>/out/error); cat /proc/self/cgroup > /out/cgroup
FROM scratch
COPY --from=base /out /
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
		FrontendAttrs: map[string]string{
			"cgroup-parent": cgroupName,
		},
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
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "cgroup"))
	require.NoError(t, err)
	// cgroupns does not leak the parent cgroup name
	require.NotContains(t, strings.TrimSpace(string(dt)), `foocgroup`)

	dt, err = os.ReadFile(filepath.Join(destDir, "error"))
	require.NoError(t, err)
	require.Contains(t, strings.TrimSpace(string(dt)), `Resource temporarily unavailable`)
}

func testStepNames(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	ctx := sb.Context()

	c, err := client.New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	dockerfile := []byte(`
FROM busybox AS base
WORKDIR /out
RUN echo "base" > base
FROM scratch
COPY --from=base --chmod=0644 /out /out
`)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	f := getFrontend(t, sb)

	ch := make(chan *client.SolveStatus)

	eg, ctx := errgroup.WithContext(ctx)

	eg.Go(func() error {
		_, err = f.Solve(ctx, c, client.SolveOpt{
			LocalMounts: map[string]fsutil.FS{
				dockerui.DefaultLocalNameDockerfile: dir,
				dockerui.DefaultLocalNameContext:    dir,
			},
		}, ch)
		return err
	})

	eg.Go(func() error {
		hasCopy := false
		hasRun := false
		visited := make(map[string]struct{})
		for status := range ch {
			for _, vtx := range status.Vertexes {
				if _, ok := visited[vtx.Name]; ok {
					continue
				}
				visited[vtx.Name] = struct{}{}
				t.Logf("step: %q", vtx.Name)
				switch vtx.Name {
				case `[base 3/3] RUN echo "base" > base`:
					hasRun = true
				case `[stage-1 1/1] COPY --from=base --chmod=0644 /out /out`:
					hasCopy = true
				}
			}
		}
		if !hasCopy {
			return errors.New("missing copy step")
		}
		if !hasRun {
			return errors.New("missing run step")
		}
		return nil
	})

	err = eg.Wait()
	require.NoError(t, err)
}

func testSourceDateEpochWithoutExporter(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	workers.CheckFeatureCompat(t, sb, workers.FeatureOCIExporter, workers.FeatureSourceDateEpoch)
	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM scratch
ENTRYPOINT foo bar
COPY Dockerfile .
`)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	destDir, err := os.MkdirTemp("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(destDir)

	out := filepath.Join(destDir, "out.tar")
	outW, err := os.Create(out)
	require.NoError(t, err)

	tm := time.Date(2015, time.October, 21, 7, 28, 0, 0, time.UTC)

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"build-arg:SOURCE_DATE_EPOCH": fmt.Sprintf("%d", tm.Unix()),
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
		Exports: []client.ExportEntry{
			{
				Type: client.ExporterOCI,
				// disable exporter epoch to make sure we test dockerfile
				Attrs:  map[string]string{"source-date-epoch": ""},
				Output: fixedWriteCloser(outW),
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "out.tar"))
	require.NoError(t, err)

	m, err := testutil.ReadTarToMap(dt, false)
	require.NoError(t, err)

	var idx ocispecs.Index
	err = json.Unmarshal(m[ocispecs.ImageIndexFile].Data, &idx)
	require.NoError(t, err)

	mlistHex := idx.Manifests[0].Digest.Hex()

	var mfst ocispecs.Manifest
	err = json.Unmarshal(m[ocispecs.ImageBlobsDir+"/sha256/"+mlistHex].Data, &mfst)
	require.NoError(t, err)

	var img ocispecs.Image
	err = json.Unmarshal(m[ocispecs.ImageBlobsDir+"/sha256/"+mfst.Config.Digest.Hex()].Data, &img)
	require.NoError(t, err)

	require.Equal(t, tm.Unix(), img.Created.Unix())
	for _, h := range img.History {
		require.Equal(t, tm.Unix(), h.Created.Unix())
	}
}

// moby/buildkit#5572
func testOCILayoutMultiname(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")

	workers.CheckFeatureCompat(t, sb, workers.FeatureOCIExporter)

	ctx := sb.Context()

	c, err := client.New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM scratch
COPY <<EOF /foo
hello
EOF
`)
	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	dest := t.TempDir()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
		Exports: []client.ExportEntry{
			{
				Type:      client.ExporterOCI,
				OutputDir: dest,
				Attrs: map[string]string{
					"tar":  "false",
					"name": "org/repo:tag1,org/repo:tag2",
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	var idx ocispecs.Index
	dt, err := os.ReadFile(filepath.Join(dest, "index.json"))
	require.NoError(t, err)

	err = json.Unmarshal(dt, &idx)
	require.NoError(t, err)

	validateIdx := func(idx ocispecs.Index) {
		require.Equal(t, 2, len(idx.Manifests))

		require.Equal(t, idx.Manifests[0].Digest, idx.Manifests[1].Digest)
		require.Equal(t, idx.Manifests[0].Platform, idx.Manifests[1].Platform)
		require.Equal(t, idx.Manifests[0].MediaType, idx.Manifests[1].MediaType)
		require.Equal(t, idx.Manifests[0].Size, idx.Manifests[1].Size)

		require.Equal(t, "docker.io/org/repo:tag1", idx.Manifests[0].Annotations["io.containerd.image.name"])
		require.Equal(t, "docker.io/org/repo:tag2", idx.Manifests[1].Annotations["io.containerd.image.name"])

		require.Equal(t, "tag1", idx.Manifests[0].Annotations["org.opencontainers.image.ref.name"])
		require.Equal(t, "tag2", idx.Manifests[1].Annotations["org.opencontainers.image.ref.name"])
	}
	validateIdx(idx)

	// test that tar variant matches
	buf := &bytes.Buffer{}
	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
		Exports: []client.ExportEntry{
			{
				Type:   client.ExporterOCI,
				Output: fixedWriteCloser(&nopWriteCloser{buf}),
				Attrs: map[string]string{
					"name": "org/repo:tag1,org/repo:tag2",
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	m, err := testutil.ReadTarToMap(buf.Bytes(), false)
	require.NoError(t, err)

	var idx2 ocispecs.Index
	err = json.Unmarshal(m["index.json"].Data, &idx2)
	require.NoError(t, err)

	validateIdx(idx2)
}

func testReproSourceDateEpoch(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	workers.CheckFeatureCompat(t, sb, workers.FeatureOCIExporter, workers.FeatureSourceDateEpoch)
	if sb.Snapshotter() == "native" {
		t.Skip("the digest is not reproducible with the \"native\" snapshotter because hardlinks are processed in a different way: https://github.com/moby/buildkit/pull/3456#discussion_r1062650263")
	}

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	f := getFrontend(t, sb)

	tm := time.Date(2023, time.January, 10, 12, 34, 56, 0, time.UTC) // 1673354096
	t.Logf("SOURCE_DATE_EPOCH=%d", tm.Unix())

	type testCase struct {
		name           string
		dockerfile     string
		files          []fstest.Applier
		expectedDigest string
		noCacheExport  bool
	}
	testCases := []testCase{
		{
			name: "Basic",
			dockerfile: `# The base image could not be busybox, due to https://github.com/moby/buildkit/issues/3455
FROM amd64/debian:bullseye-20230109-slim
RUN touch /foo
RUN touch /foo.1
RUN touch -d '2010-01-01 12:34:56' /foo-2010
RUN touch -d '2010-01-01 12:34:56' /foo-2010.1
RUN touch -d '2030-01-01 12:34:56' /foo-2030
RUN touch -d '2030-01-01 12:34:56' /foo-2030.1
RUN rm -f /foo.1
RUN rm -f /foo-2010.1
RUN rm -f /foo-2030.1
`,
			expectedDigest: "sha256:04e5d0cbee3317c79f50494cfeb4d8a728402a970ef32582ee47c62050037e3f",
		},
		{
			// https://github.com/moby/buildkit/issues/4746
			name: "CopyLink",
			dockerfile: `FROM amd64/debian:bullseye-20230109-slim
COPY --link foo foo
`,
			files:          []fstest.Applier{fstest.CreateFile("foo", []byte("foo"), 0600)},
			expectedDigest: "sha256:9f75e4bdbf3d825acb36bb603ddef4a25742afb8ccb674763ffc611ae047d8a6",
		},
		{
			// https://github.com/moby/buildkit/issues/4793
			name: "NoAdditionalLayer",
			dockerfile: `FROM amd64/debian:bullseye-20230109-slim
`,
			expectedDigest: "sha256:eeba8ef81dec46359d099c5d674009da54e088fa8f29945d4d7fb3a7a88c450e",
			noCacheExport:  true, // "skipping cache export for empty result"
		},
	}

	// https://explore.ggcr.dev/?image=amd64%2Fdebian%3Abullseye-20230109-slim
	baseImageLayers := []digest.Digest{
		"sha256:8740c948ffd4c816ea7ca963f99ca52f4788baa23f228da9581a9ea2edd3fcd7",
	}
	baseImageHistoryTimestamps := []time.Time{
		timeMustParse(t, time.RFC3339Nano, "2023-01-11T02:34:44.402266175Z"),
		timeMustParse(t, time.RFC3339Nano, "2023-01-11T02:34:44.829692296Z"),
	}

	ctx := sb.Context()
	c, err := client.New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			dir := integration.Tmpdir(
				t,
				append([]fstest.Applier{fstest.CreateFile("Dockerfile", []byte(tc.dockerfile), 0600)}, tc.files...)...,
			)

			target := registry + "/buildkit/testreprosourcedateepoch-" + strings.ToLower(tc.name) + ":" + fmt.Sprintf("%d", tm.Unix())
			solveOpt := client.SolveOpt{
				FrontendAttrs: map[string]string{
					"build-arg:SOURCE_DATE_EPOCH": fmt.Sprintf("%d", tm.Unix()),
					"platform":                    "linux/amd64",
				},
				LocalMounts: map[string]fsutil.FS{
					dockerui.DefaultLocalNameDockerfile: dir,
					dockerui.DefaultLocalNameContext:    dir,
				},
				Exports: []client.ExportEntry{
					{
						Type: client.ExporterImage,
						Attrs: map[string]string{
							"name":              target,
							"push":              "true",
							"oci-mediatypes":    "true",
							"rewrite-timestamp": "true",
						},
					},
				},
				CacheExports: []client.CacheOptionsEntry{
					{
						Type: "registry",
						Attrs: map[string]string{
							"ref":            target + "-cache",
							"oci-mediatypes": "true",
						},
					},
				},
			}
			_, err = f.Solve(ctx, c, solveOpt, nil)
			require.NoError(t, err)

			desc, manifest, img := readImage(t, ctx, target)
			var cacheManifest ocispecs.Manifest
			if !tc.noCacheExport {
				_, cacheManifest, _ = readImage(t, ctx, target+"-cache")
			}
			t.Log("The digest may change depending on the BuildKit version, the snapshotter configuration, etc.")
			require.Equal(t, tc.expectedDigest, desc.Digest.String())

			// Image history from the base config must remain immutable
			for i, tm := range baseImageHistoryTimestamps {
				require.True(t, img.History[i].Created.Equal(tm))
			}

			// Image layers, *except the base layers*, must have rewritten-timestamp
			for i, l := range manifest.Layers {
				if i < len(baseImageLayers) {
					require.Empty(t, l.Annotations["buildkit/rewritten-timestamp"])
					require.Equal(t, baseImageLayers[i], l.Digest)
				} else {
					require.Equal(t, fmt.Sprintf("%d", tm.Unix()), l.Annotations["buildkit/rewritten-timestamp"])
				}
			}
			if !tc.noCacheExport {
				// Cache layers must *not* have rewritten-timestamp
				for _, l := range cacheManifest.Layers {
					require.Empty(t, l.Annotations["buildkit/rewritten-timestamp"])
				}
			}

			// Build again, after pruning the base image layer cache.
			// For testing https://github.com/moby/buildkit/issues/4746
			ensurePruneAll(t, c, sb)
			_, err = f.Solve(ctx, c, solveOpt, nil)
			require.NoError(t, err)
			descAfterPrune, _, _ := readImage(t, ctx, target)
			require.Equal(t, desc.Digest.String(), descAfterPrune.Digest.String())

			// Build again, but without rewrite-timestamp
			solveOpt2 := solveOpt
			delete(solveOpt2.Exports[0].Attrs, "rewrite-timestamp")
			_, err = f.Solve(ctx, c, solveOpt2, nil)
			require.NoError(t, err)
			_, manifest2, img2 := readImage(t, ctx, target)
			for i, tm := range baseImageHistoryTimestamps {
				require.True(t, img2.History[i].Created.Equal(tm))
			}
			for _, l := range manifest2.Layers {
				require.Empty(t, l.Annotations["buildkit/rewritten-timestamp"])
			}
		})
	}
}

func testMultiNilRefsOCIExporter(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	workers.CheckFeatureCompat(t, sb, workers.FeatureMultiPlatform, workers.FeatureOCIExporter)

	f := getFrontend(t, sb)

	dockerfile := []byte(`FROM scratch`)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	destDir := t.TempDir()

	out := filepath.Join(destDir, "out.tar")
	outW, err := os.Create(out)
	require.NoError(t, err)

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
		FrontendAttrs: map[string]string{
			"platform": "linux/arm64,linux/amd64",
		},
		Exports: []client.ExportEntry{
			{
				Type:   client.ExporterOCI,
				Output: fixedWriteCloser(outW),
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "out.tar"))
	require.NoError(t, err)

	m, err := testutil.ReadTarToMap(dt, false)
	require.NoError(t, err)

	var idx ocispecs.Index
	err = json.Unmarshal(m[ocispecs.ImageIndexFile].Data, &idx)
	require.NoError(t, err)

	require.Equal(t, 1, len(idx.Manifests))
	mlistHex := idx.Manifests[0].Digest.Hex()

	idx = ocispecs.Index{}
	err = json.Unmarshal(m[ocispecs.ImageBlobsDir+"/sha256/"+mlistHex].Data, &idx)
	require.NoError(t, err)

	require.Equal(t, 2, len(idx.Manifests))
}

func timeMustParse(t *testing.T, layout, value string) time.Time {
	tm, err := time.Parse(layout, value)
	require.NoError(t, err)
	return tm
}

//nolint:revive // context-as-argument: context.Context should be the first parameter of a function
func readImage(t *testing.T, ctx context.Context, ref string) (ocispecs.Descriptor, ocispecs.Manifest, ocispecs.Image) {
	desc, provider, err := contentutil.ProviderFromRef(ref)
	require.NoError(t, err)
	dt, err := content.ReadBlob(ctx, provider, desc)
	require.NoError(t, err)
	var manifest ocispecs.Manifest
	require.NoError(t, json.Unmarshal(dt, &manifest))
	imgDt, err := content.ReadBlob(ctx, provider, manifest.Config)
	require.NoError(t, err)
	// Verify that all the layer blobs are present
	for _, layer := range manifest.Layers {
		layerRA, err := provider.ReaderAt(ctx, layer)
		require.NoError(t, err)
		layerDigest, err := layer.Digest.Algorithm().FromReader(content.NewReader(layerRA))
		require.NoError(t, err)
		require.Equal(t, layer.Digest, layerDigest)
	}
	var img ocispecs.Image
	require.NoError(t, json.Unmarshal(imgDt, &img))
	return desc, manifest, img
}

func testNilContextInSolveGateway(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)
	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	_, err = c.Build(sb.Context(), client.SolveOpt{}, "", func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		res, err := f.SolveGateway(ctx, c, gateway.SolveRequest{
			Frontend: "dockerfile.v0",
			FrontendInputs: map[string]*pb.Definition{
				dockerui.DefaultLocalNameContext:    nil,
				dockerui.DefaultLocalNameDockerfile: nil,
			},
		})
		if err != nil {
			return nil, err
		}
		return res, nil
	}, nil)
	// should not cause buildkitd to panic
	require.ErrorContains(t, err, "invalid nil input definition to definition op")
}

func testMultiNilRefsInSolveGateway(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureMultiPlatform)
	ctx := sb.Context()

	c, err := client.New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	f := getFrontend(t, sb)

	_, err = c.Build(sb.Context(), client.SolveOpt{}, "", func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		localDockerfile, err := llb.Scratch().
			File(llb.Mkfile("Dockerfile", 0644, []byte(`FROM scratch`))).
			Marshal(ctx)
		if err != nil {
			return nil, err
		}

		res, err := f.SolveGateway(ctx, c, gateway.SolveRequest{
			Frontend: "dockerfile.v0",
			FrontendOpt: map[string]string{
				"platform": "linux/amd64,linux/arm64",
			},
			FrontendInputs: map[string]*pb.Definition{
				dockerui.DefaultLocalNameDockerfile: localDockerfile.ToPB(),
			},
		})
		if err != nil {
			return nil, err
		}
		return res, nil
	}, nil)
	require.NoError(t, err)
}

func testBaseImagePlatformMismatch(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	workers.CheckFeatureCompat(t, sb, workers.FeatureDirectPush)
	ctx := sb.Context()

	c, err := client.New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM scratch
COPY foo /foo
`)
	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile("foo", []byte("test"), 0644),
	)

	// choose target platform that is different from the current platform
	targetPlatform := runtime.GOOS + "/arm64"
	if runtime.GOARCH == "arm64" {
		targetPlatform = runtime.GOOS + "/amd64"
	}

	target := registry + "/buildkit/testbaseimageplatform:latest"
	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
		FrontendAttrs: map[string]string{
			"platform": targetPlatform,
		},
		Exports: []client.ExportEntry{
			{
				Type: client.ExporterImage,
				Attrs: map[string]string{
					"name": target,
					"push": "true",
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	dockerfile = fmt.Appendf(nil, `
FROM %s
ENV foo=bar
	`, target)

	checkLinterWarnings(t, sb, &lintTestParams{
		Dockerfile: dockerfile,
		Warnings: []expectedLintWarning{
			{
				RuleName:    "InvalidBaseImagePlatform",
				Description: "Base image platform does not match expected target platform",
				Detail:      fmt.Sprintf("Base image %s was pulled with platform %q, expected %q for current build", target, targetPlatform, runtime.GOOS+"/"+runtime.GOARCH),
				Level:       1,
				Line:        2,
			},
		},
		FrontendAttrs: map[string]string{},
	})
}

func testPlatformWithOSVersion(t *testing.T, sb integration.Sandbox) {
	// This test cannot be run on Windows currently due to `FROM scratch` and
	// layer formatting not being supported on Windows.
	integration.SkipOnPlatform(t, "windows")
	workers.CheckFeatureCompat(t, sb, workers.FeatureDirectPush)

	ctx := sb.Context()

	c, err := client.New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	f := getFrontend(t, sb)

	// NOTE: currently "OS" *must* be set to "windows" for this to work.
	// The platform matchers only do OSVersion comparisons when the OS is set to "windows".
	p1 := ocispecs.Platform{
		OS:           "windows",
		OSVersion:    "1.2.3",
		Architecture: "bar",
	}
	p2 := ocispecs.Platform{
		OS:           "windows",
		OSVersion:    "1.1.0",
		Architecture: "bar",
	}

	p1Str := platforms.FormatAll(p1)
	p2Str := platforms.FormatAll(p2)

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)
	target := registry + "/buildkit/testplatformwithosversion:latest"

	dockerfile := []byte(`
FROM ` + target + ` AS reg

FROM scratch AS base
ARG TARGETOSVERSION
COPY <<EOF /osversion
${TARGETOSVERSION}
EOF
ARG TARGETPLATFORM
COPY <<EOF /targetplatform
${TARGETPLATFORM}
EOF
`)

	destDir := t.TempDir()
	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	// build the base target as a multi-platform image and push to the registry
	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"platform": p1Str + "," + p2Str,
			"target":   "base",
		},
		Exports: []client.ExportEntry{
			{
				Type:      client.ExporterLocal,
				OutputDir: destDir,
			},
			{
				Type: client.ExporterImage,
				Attrs: map[string]string{
					"name": target,
					"push": "true",
				},
			},
		},

		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)

	require.NoError(t, err)

	desc, provider, err := contentutil.ProviderFromRef(target)
	require.NoError(t, err)

	info, err := testutil.ReadImages(ctx, provider, desc)
	require.NoError(t, err)
	require.Len(t, info.Images, 2)
	require.Equal(t, info.Images[0].Img.OSVersion, p1.OSVersion)
	require.Equal(t, info.Images[1].Img.OSVersion, p2.OSVersion)

	dt, err := os.ReadFile(filepath.Join(destDir, strings.Replace(p1Str, "/", "_", 1), "osversion"))
	require.NoError(t, err)
	require.Equal(t, p1.OSVersion+"\n", string(dt))

	dt, err = os.ReadFile(filepath.Join(destDir, strings.Replace(p1Str, "/", "_", 1), "targetplatform"))
	require.NoError(t, err)
	require.Equal(t, p1Str+"\n", string(dt))

	dt, err = os.ReadFile(filepath.Join(destDir, strings.Replace(p2Str, "/", "_", 1), "osversion"))
	require.NoError(t, err)
	require.Equal(t, p2.OSVersion+"\n", string(dt))

	dt, err = os.ReadFile(filepath.Join(destDir, strings.Replace(p2Str, "/", "_", 1), "targetplatform"))
	require.NoError(t, err)
	require.Equal(t, p2Str+"\n", string(dt))

	// Now build the "reg" target, which should pull the base image from the registry
	// This should select the image with the requested os version.
	destDir = t.TempDir()
	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"platform": p1Str,
			"target":   "reg",
		},
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

	dt, err = os.ReadFile(filepath.Join(destDir, "osversion"))
	require.NoError(t, err)
	require.Equal(t, p1.OSVersion+"\n", string(dt))

	// And again with the other os version
	destDir = t.TempDir()
	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"platform": p2Str,
			"target":   "reg",
		},
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

	dt, err = os.ReadFile(filepath.Join(destDir, "osversion"))
	require.NoError(t, err)
	require.Equal(t, p2.OSVersion+"\n", string(dt))
}

func testMaintainBaseOSVersion(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	workers.CheckFeatureCompat(t, sb, workers.FeatureDirectPush)

	ctx := sb.Context()

	c, err := client.New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	f := getFrontend(t, sb)

	p1 := ocispecs.Platform{
		OS:           "windows",
		OSVersion:    "10.20.30",
		Architecture: "amd64",
	}
	p1Str := platforms.FormatAll(p1)

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)
	target := registry + "/buildkit/testplatformwithosversion-1:latest"

	dockerfile := []byte(`
FROM scratch
ARG TARGETPLATFORM
COPY <<EOF /platform
${TARGETPLATFORM}
EOF
`)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"platform": p1Str,
		},
		Exports: []client.ExportEntry{
			{
				Type: client.ExporterImage,
				Attrs: map[string]string{
					"name": target,
					"push": "true",
				},
			},
		},

		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)

	require.NoError(t, err)

	desc, provider, err := contentutil.ProviderFromRef(target)
	require.NoError(t, err)

	info, err := testutil.ReadImages(ctx, provider, desc)
	require.NoError(t, err)
	require.Len(t, info.Images, 1)
	require.Equal(t, info.Images[0].Img.OSVersion, p1.OSVersion)

	dockerfile = fmt.Appendf(nil, `
FROM %s
COPY <<EOF /other
hello
EOF
`, target)

	dir = integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	target2 := registry + "/buildkit/testplatformwithosversion-2:latest"

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"platform": p1.OS + "/" + p1.Architecture,
		},
		Exports: []client.ExportEntry{
			{
				Type: client.ExporterImage,
				Attrs: map[string]string{
					"name": target2,
					"push": "true",
				},
			},
		},

		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)

	desc, provider, err = contentutil.ProviderFromRef(target2)
	require.NoError(t, err)

	info, err = testutil.ReadImages(ctx, provider, desc)
	require.NoError(t, err)
	require.Len(t, info.Images, 1)
	require.Equal(t, info.Images[0].Img.OSVersion, p1.OSVersion)
}

func testTargetMistype(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	workers.CheckFeatureCompat(t, sb, workers.FeatureDirectPush)

	ctx := sb.Context()

	c, err := client.New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM scratch AS build
COPY Dockerfile /out

FROM scratch
COPY --from=build /out /
`)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"target": "bulid",
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)

	require.Error(t, err)
	require.Contains(t, err.Error(), "target stage \"bulid\" could not be found (did you mean build?)")
}

func runShell(dir string, cmds ...string) error {
	for _, args := range cmds {
		var cmd *exec.Cmd
		if runtime.GOOS == "windows" {
			cmd = exec.Command("powershell", "-command", args)
		} else {
			cmd = exec.Command("sh", "-c", args)
		}
		cmd.Dir = dir
		if err := cmd.Run(); err != nil {
			return errors.Wrapf(err, "error running %v", args)
		}
	}
	return nil
}

// ensurePruneAll tries to ensure Prune completes with retries.
// Current cache implementation defers release-related logic using goroutine so
// there can be situation where a build has finished but the following prune doesn't
// cleanup cache because some records still haven't been released.
// This function tries to ensure prune by retrying it.
func ensurePruneAll(t *testing.T, c *client.Client, sb integration.Sandbox) {
	for i := range 2 {
		require.NoError(t, c.Prune(sb.Context(), nil, client.PruneAll))
		for range 20 {
			du, err := c.DiskUsage(sb.Context())
			require.NoError(t, err)
			if len(du) == 0 {
				return
			}
			time.Sleep(500 * time.Millisecond)
		}
		t.Logf("retrying prune(%d)", i)
	}
	t.Fatalf("failed to ensure prune")
}

func checkAllReleasable(t *testing.T, c *client.Client, sb integration.Sandbox, checkContent bool) {
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

	retries := 0
loop0:
	for {
		require.Less(t, retries, 20)
		retries++
		du, err := c.DiskUsage(sb.Context())
		require.NoError(t, err)
		for _, d := range du {
			if d.InUse {
				time.Sleep(500 * time.Millisecond)
				continue loop0
			}
		}
		break
	}

	err = c.Prune(sb.Context(), nil, client.PruneAll)
	require.NoError(t, err)

	du, err := c.DiskUsage(sb.Context())
	require.NoError(t, err)
	require.Equal(t, 0, len(du))

	// examine contents of exported tars (requires containerd)
	cdAddress := sb.ContainerdAddress()
	if cdAddress == "" {
		return
	}

	// TODO: make public pull helper function so this can be checked for standalone as well

	client, err := newContainerd(cdAddress)
	require.NoError(t, err)
	defer client.Close()

	ctx := namespaces.WithNamespace(sb.Context(), "buildkit")
	// pick default snapshotter on Windows, hence ""
	snapshotName := integration.UnixOrWindows("overlayfs", "")
	snapshotService := client.SnapshotService(snapshotName)

	retries = 0
	for {
		count := 0
		err = snapshotService.Walk(ctx, func(context.Context, snapshots.Info) error {
			count++
			return nil
		})
		require.NoError(t, err)
		if count == 0 {
			break
		}
		require.Less(t, retries, 20)
		retries++
		time.Sleep(500 * time.Millisecond)
	}

	if !checkContent {
		return
	}

	retries = 0
	for {
		count := 0
		err = client.ContentStore().Walk(ctx, func(content.Info) error {
			count++
			return nil
		})
		require.NoError(t, err)
		if count == 0 {
			break
		}
		require.Less(t, retries, 20)
		retries++
		time.Sleep(500 * time.Millisecond)
	}
}

func newContainerd(cdAddress string) (*ctd.Client, error) {
	return ctd.New(cdAddress, ctd.WithTimeout(60*time.Second))
}

func dfCmdArgs(ctx, dockerfile, args string) (string, string) {
	traceFile := filepath.Join(os.TempDir(), "trace"+identity.NewID())
	return fmt.Sprintf("build --progress=plain %s --local context=%s --local dockerfile=%s --trace=%s", args, ctx, dockerfile, traceFile), traceFile
}

type builtinFrontend struct{}

var _ frontend = &builtinFrontend{}

func (f *builtinFrontend) Solve(ctx context.Context, c *client.Client, opt client.SolveOpt, statusChan chan *client.SolveStatus) (*client.SolveResponse, error) {
	opt.Frontend = "dockerfile.v0"
	return c.Solve(ctx, nil, opt, statusChan)
}

func (f *builtinFrontend) SolveGateway(ctx context.Context, c gateway.Client, req gateway.SolveRequest) (*gateway.Result, error) {
	req.Frontend = "dockerfile.v0"
	return c.Solve(ctx, req)
}

func (f *builtinFrontend) DFCmdArgs(ctx, dockerfile string) (string, string) {
	return dfCmdArgs(ctx, dockerfile, "--frontend dockerfile.v0")
}

func (f *builtinFrontend) RequiresBuildctl(t *testing.T) {}

type clientFrontend struct{}

var _ frontend = &clientFrontend{}

func (f *clientFrontend) Solve(ctx context.Context, c *client.Client, opt client.SolveOpt, statusChan chan *client.SolveStatus) (*client.SolveResponse, error) {
	return c.Build(ctx, opt, "", builder.Build, statusChan)
}

func (f *clientFrontend) SolveGateway(ctx context.Context, c gateway.Client, req gateway.SolveRequest) (*gateway.Result, error) {
	if req.Frontend == "" && req.Definition == nil {
		req.Frontend = "dockerfile.v0"
	}
	return c.Solve(ctx, req)
}

func (f *clientFrontend) DFCmdArgs(ctx, dockerfile string) (string, string) {
	return "", ""
}

func (f *clientFrontend) RequiresBuildctl(t *testing.T) {
	t.Skip()
}

type gatewayFrontend struct {
	gw string
}

var _ frontend = &gatewayFrontend{}

func (f *gatewayFrontend) Solve(ctx context.Context, c *client.Client, opt client.SolveOpt, statusChan chan *client.SolveStatus) (*client.SolveResponse, error) {
	opt.Frontend = "gateway.v0"
	if opt.FrontendAttrs == nil {
		opt.FrontendAttrs = make(map[string]string)
	}
	opt.FrontendAttrs["source"] = f.gw
	return c.Solve(ctx, nil, opt, statusChan)
}

func (f *gatewayFrontend) SolveGateway(ctx context.Context, c gateway.Client, req gateway.SolveRequest) (*gateway.Result, error) {
	req.Frontend = "gateway.v0"
	if req.FrontendOpt == nil {
		req.FrontendOpt = make(map[string]string)
	}
	req.FrontendOpt["source"] = f.gw
	return c.Solve(ctx, req)
}

func (f *gatewayFrontend) DFCmdArgs(ctx, dockerfile string) (string, string) {
	return dfCmdArgs(ctx, dockerfile, "--frontend gateway.v0 --opt=source="+f.gw)
}

func (f *gatewayFrontend) RequiresBuildctl(t *testing.T) {}

func getFrontend(t *testing.T, sb integration.Sandbox) frontend {
	v := sb.Value("frontend")
	require.NotNil(t, v)
	fn, ok := v.(frontend)
	require.True(t, ok)
	return fn
}

type nopWriteCloser struct {
	io.Writer
}

func (nopWriteCloser) Close() error { return nil }

type secModeSandbox struct{}

func (*secModeSandbox) UpdateConfigFile(in string) string {
	return in
}

type secModeInsecure struct{}

func (*secModeInsecure) UpdateConfigFile(in string) string {
	return in + "\n\ninsecure-entitlements = [\"security.insecure\"]\n"
}

var (
	securityInsecureGranted integration.ConfigUpdater = &secModeInsecure{}
	securityInsecureDenied  integration.ConfigUpdater = &secModeSandbox{}
)

type networkModeHost struct{}

func (*networkModeHost) UpdateConfigFile(in string) string {
	return in + "\n\ninsecure-entitlements = [\"network.host\"]\n"
}

type networkModeSandbox struct{}

func (*networkModeSandbox) UpdateConfigFile(in string) string {
	return in
}

var (
	networkHostGranted integration.ConfigUpdater = &networkModeHost{}
	networkHostDenied  integration.ConfigUpdater = &networkModeSandbox{}
)

func fixedWriteCloser(wc io.WriteCloser) filesync.FileOutputFunc {
	return func(map[string]string) (io.WriteCloser, error) {
		return wc, nil
	}
}
