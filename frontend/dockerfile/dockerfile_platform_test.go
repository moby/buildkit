package dockerfile

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/containerd/continuity/fs/fstest"
	"github.com/containerd/platforms"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/frontend/dockerui"
	"github.com/moby/buildkit/util/contentutil"
	"github.com/moby/buildkit/util/testutil"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/moby/buildkit/util/testutil/workers"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	"github.com/tonistiigi/fsutil"
)

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
	f := getFrontend(t, sb)

	baseImage := integration.UnixOrWindows("busybox", "nanoserver:latest")
	mkdirCmd := integration.UnixOrWindows("mkdir /out", "mkdir out")
	echoCmd := integration.UnixOrWindows(
		"echo -n $TARGETPLATFORM > /out/platform && echo -n $TARGETOS > /out/os",
		"echo %TARGETPLATFORM%> out\\platform & echo %TARGETOS%> out\\os",
	)

	dockerfile := fmt.Appendf(nil, `
FROM --platform=$BUILDPLATFORM %s AS build
ARG TARGETPLATFORM
ARG TARGETOS
RUN %s && %s
FROM scratch
COPY --from=build out .
`, baseImage, mkdirCmd, echoCmd)

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
	platformStr := integration.UnixOrWindows(string(dt), strings.TrimSpace(string(dt)))
	require.Equal(t, "darwin/ppc64le", platformStr)

	dt, err = os.ReadFile(filepath.Join(destDir, "os"))
	require.NoError(t, err)
	osStr := integration.UnixOrWindows(string(dt), strings.TrimSpace(string(dt)))
	require.Equal(t, "freebsd", osStr)
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
	workers.CheckFeatureCompat(t, sb, workers.FeatureDirectPush)

	ctx := sb.Context()

	c, err := client.New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	f := getFrontend(t, sb)

	p1 := ocispecs.Platform{
		OS:           "windows",
		OSVersion:    "10.0.20348.1006",
		Architecture: "amd64",
	}
	p1Str := platforms.FormatAll(p1)

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)
	target := registry + "/buildkit/testplatformwithosversion-1:latest"

	dockerfile := []byte(integration.UnixOrWindows(`
FROM scratch
ARG TARGETPLATFORM
COPY <<EOF /platform
${TARGETPLATFORM}
EOF
`, `
FROM nanoserver:latest
ARG TARGETPLATFORM
COPY <<EOF C:\platform
${TARGETPLATFORM}
EOF
`))

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

	dockerfile = []byte(integration.UnixOrWindows(fmt.Sprintf(`
FROM %s
COPY <<EOF /other
hello
EOF
`, target), fmt.Sprintf(`
FROM %s
COPY <<EOF C:/other
hello
EOF
`, target)))

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
