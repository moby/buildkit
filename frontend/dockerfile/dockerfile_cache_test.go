package dockerfile

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/snapshots"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/continuity/fs/fstest"
	controlapi "github.com/moby/buildkit/api/services/control"
	cacheimporttypes "github.com/moby/buildkit/cache/remotecache/v1/types"
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

func testCacheReleased(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM busybox
`,
		`
FROM nanoserver
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

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)

	checkAllReleasable(t, c, sb, true)
}

func testExportCacheLoop(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureCacheExport, workers.FeatureCacheImport, workers.FeatureCacheBackendLocal)
	f := getFrontend(t, sb)

	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM alpine as base
RUN echo aa > /foo
WORKDIR /bar

FROM base as base1
COPY hello.txt .

FROM base as base2
COPY --from=base1 /bar/hello.txt .
RUN true

FROM scratch
COPY --from=base2 /foo /f
`,
		`
FROM nanoserver AS base
USER ContainerAdministrator
RUN echo aa> foo
WORKDIR /bar

FROM base AS base1
USER ContainerAdministrator
COPY hello.txt .

FROM base AS base2
USER ContainerAdministrator
COPY --from=base1 /bar/hello.txt .

FROM nanoserver
COPY --from=base2 foo f
`,
	))

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile("hello.txt", []byte("hello"), 0600),
	)

	cacheDir := t.TempDir()

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
		CacheExports: []client.CacheOptionsEntry{
			{
				Type: "local",
				Attrs: map[string]string{
					"dest": filepath.Join(cacheDir, "cache"),
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	err = c.Prune(sb.Context(), nil)
	require.NoError(t, err)

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
		CacheExports: []client.CacheOptionsEntry{
			{
				Type: "local",
				Attrs: map[string]string{
					"dest": filepath.Join(cacheDir, "cache"),
				},
			},
		},
		CacheImports: []client.CacheOptionsEntry{
			{
				Type: "local",
				Attrs: map[string]string{
					"src": filepath.Join(cacheDir, "cache"),
				},
			},
		},
		FrontendAttrs: map[string]string{},
	}, nil)
	require.NoError(t, err)
}

// testCacheMultiPlatformImportExport checks that the build cache works across
// multiple platforms at once.
//
// It builds the same Dockerfile for two platforms, pushes the result to a
// registry with an inline cache, and then rebuilds from that cache twice. Each
// rebuild must produce exactly the same image (same digest and same file
// contents per platform) as the first build, proving the cache was reused
// instead of re-running the build steps.
func testCacheMultiPlatformImportExport(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb,
		workers.FeatureDirectPush,
		workers.FeatureCacheExport,
		workers.FeatureCacheBackendInline,
		workers.FeatureCacheBackendRegistry,
	)
	f := getFrontend(t, sb)

	// Two-platform matrix per OS. TARGETARCH for linux/arm/v7 is "arm";
	// for windows/arm64 it is "arm64".
	platformAttr := integration.UnixOrWindows("linux/amd64,linux/arm/v7", "windows/amd64,windows/arm64")
	platform1 := integration.UnixOrWindows("linux/amd64", "windows/amd64")
	platform2 := integration.UnixOrWindows("linux/arm/v7", "windows/arm64")
	arch2 := integration.UnixOrWindows("arm", "arm64")

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	// Windows equivalent of the Linux RUN: nanoserver lacks PowerShell, sha256sum,
	// and /dev/urandom, so use cmd.exe with TARGETARCH+RANDOM+TIME for uniqueness.
	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM --platform=$BUILDPLATFORM busybox AS base
ARG TARGETARCH
RUN echo -n $TARGETARCH> arch && cat /dev/urandom | head -c 100 | sha256sum > unique
FROM scratch
COPY --from=base unique /
COPY --from=base arch /
`,
		`
FROM --platform=$BUILDPLATFORM nanoserver AS base
USER ContainerAdministrator
ARG TARGETARCH
RUN ["cmd","/S","/C","echo %TARGETARCH%>C:\\arch & echo %TARGETARCH%-%RANDOM%-%RANDOM%-%RANDOM%-%TIME%-%DATE%>C:\\unique"]
FROM nanoserver
COPY --from=base /unique /
COPY --from=base /arch /
`,
	))

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	target := registry + "/buildkit/testexportdf:multi"

	exportCache := []client.CacheOptionsEntry{
		{
			Type: "inline",
		},
	}
	importCache := target + "-img"

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		Exports: []client.ExportEntry{
			{
				Type: client.ExporterImage,
				Attrs: map[string]string{
					"push": "true",
					"name": target + "-img",
				},
			},
		},
		CacheExports: exportCache,
		FrontendAttrs: map[string]string{
			"platform": platformAttr,
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)

	desc, provider, err := contentutil.ProviderFromRef(target + "-img")
	require.NoError(t, err)

	imgs, err := testutil.ReadImages(sb.Context(), provider, desc)
	require.NoError(t, err)

	require.Equal(t, 2, len(imgs.Images))

	// Windows layers: nanoserver adds 1 base OS layer (shifting indices by +1)
	// and tar entries are nested under "Files/".
	archLayerIdx := integration.UnixOrWindows(1, 2)
	uniqueLayerIdx := integration.UnixOrWindows(0, 1)
	archKey := integration.UnixOrWindows("arch", "Files/arch")
	uniqueKey := integration.UnixOrWindows("unique", "Files/unique")

	require.Equal(t, "amd64", strings.TrimSpace(string(imgs.Find(platform1).Layers[archLayerIdx][archKey].Data)))
	dtamd := imgs.Find(platform1).Layers[uniqueLayerIdx][uniqueKey].Data
	dtarm := imgs.Find(platform2).Layers[uniqueLayerIdx][uniqueKey].Data
	require.NotEqual(t, dtamd, dtarm)

	for range 2 {
		ensurePruneAll(t, c, sb)

		_, err = f.Solve(sb.Context(), c, client.SolveOpt{
			FrontendAttrs: map[string]string{
				"cache-from": importCache,
				"platform":   platformAttr,
			},
			Exports: []client.ExportEntry{
				{
					Type: client.ExporterImage,
					Attrs: map[string]string{
						"push": "true",
						"name": target + "-img",
					},
				},
			},
			CacheExports: exportCache,
			LocalMounts: map[string]fsutil.FS{
				dockerui.DefaultLocalNameDockerfile: dir,
				dockerui.DefaultLocalNameContext:    dir,
			},
		}, nil)
		require.NoError(t, err)

		desc2, provider, err := contentutil.ProviderFromRef(target + "-img")
		require.NoError(t, err)

		require.Equal(t, desc.Digest, desc2.Digest)

		imgs, err = testutil.ReadImages(sb.Context(), provider, desc2)
		require.NoError(t, err)

		require.Equal(t, 2, len(imgs.Images))

		require.Equal(t, arch2, strings.TrimSpace(string(imgs.Find(platform2).Layers[archLayerIdx][archKey].Data)))
		dtamd2 := imgs.Find(platform1).Layers[uniqueLayerIdx][uniqueKey].Data
		dtarm2 := imgs.Find(platform2).Layers[uniqueLayerIdx][uniqueKey].Data
		require.Equal(t, string(dtamd), string(dtamd2))
		require.Equal(t, string(dtarm), string(dtarm2))
	}
}

func testImageManifestCacheImportExport(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureCacheExport, workers.FeatureCacheBackendLocal)
	f := getFrontend(t, sb)

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	dockerfile := integration.UnixOrWindows([]byte(`
FROM busybox AS base
COPY foo const
#RUN echo -n foobar > const
RUN cat /dev/urandom | head -c 100 | sha256sum > unique
FROM scratch
COPY --from=base const /
COPY --from=base unique /
`), []byte(`
FROM nanoserver:latest AS base
USER ContainerAdministrator
COPY foo const
# RUN echo foobar > const
RUN echo %RANDOM%%RANDOM%> unique
FROM nanoserver:latest
COPY --from=base const /
COPY --from=base unique /
`))

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile("foo", []byte("foobar"), 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	destDir := t.TempDir()

	target := registry + "/buildkit/testexportdf:latest"

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		Exports: []client.ExportEntry{
			{
				Type:      client.ExporterLocal,
				OutputDir: destDir,
			},
		},
		CacheExports: []client.CacheOptionsEntry{
			{
				Type: "registry",
				Attrs: map[string]string{
					"ref":            target,
					"oci-mediatypes": "true",
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
	img, err := testutil.ReadImage(sb.Context(), provider, desc)
	require.NoError(t, err)

	require.Equal(t, ocispecs.MediaTypeImageManifest, img.Manifest.MediaType)
	require.Equal(t, cacheimporttypes.CacheConfigMediaTypeV0, img.Manifest.Config.MediaType)

	dt, err := os.ReadFile(filepath.Join(destDir, "const"))
	require.NoError(t, err)
	require.Equal(t, "foobar", string(dt))

	dt, err = os.ReadFile(filepath.Join(destDir, "unique"))
	require.NoError(t, err)

	ensurePruneAll(t, c, sb)

	destDir = t.TempDir()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"cache-from": target,
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

	dt2, err := os.ReadFile(filepath.Join(destDir, "const"))
	require.NoError(t, err)
	require.Equal(t, "foobar", string(dt2))

	dt2, err = os.ReadFile(filepath.Join(destDir, "unique"))
	require.NoError(t, err)
	require.Equal(t, string(dt), string(dt2))
}

func testCacheImportExport(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureCacheExport, workers.FeatureCacheBackendLocal)
	f := getFrontend(t, sb)

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	dockerfile := integration.UnixOrWindows([]byte(`
FROM busybox AS base
COPY foo const
#RUN echo -n foobar > const
RUN cat /dev/urandom | head -c 100 | sha256sum > unique
FROM scratch
COPY --from=base const /
COPY --from=base unique /
`), []byte(`
FROM nanoserver:latest AS base
USER ContainerAdministrator
COPY foo const
# RUN echo foobar > const
RUN echo %RANDOM%%RANDOM%> unique
FROM nanoserver:latest
COPY --from=base const /
COPY --from=base unique /
`))

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile("foo", []byte("foobar"), 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	destDir := t.TempDir()

	target := registry + "/buildkit/testexportdf:latest"

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		Exports: []client.ExportEntry{
			{
				Type:      client.ExporterLocal,
				OutputDir: destDir,
			},
		},
		CacheExports: []client.CacheOptionsEntry{
			{
				Type:  "registry",
				Attrs: map[string]string{"ref": target},
			},
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "const"))
	require.NoError(t, err)
	require.Equal(t, "foobar", string(dt))

	dt, err = os.ReadFile(filepath.Join(destDir, "unique"))
	require.NoError(t, err)

	ensurePruneAll(t, c, sb)

	destDir = t.TempDir()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"cache-from": target,
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

	dt2, err := os.ReadFile(filepath.Join(destDir, "const"))
	require.NoError(t, err)
	require.Equal(t, "foobar", string(dt2))

	dt2, err = os.ReadFile(filepath.Join(destDir, "unique"))
	require.NoError(t, err)
	require.Equal(t, string(dt), string(dt2))
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

func testImportExportReproducibleIDs(t *testing.T, sb integration.Sandbox) {
	cdAddress := sb.ContainerdAddress()
	if cdAddress == "" {
		t.Skip("test requires containerd worker")
	}

	f := getFrontend(t, sb)

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	dockerfile := integration.UnixOrWindows([]byte(`
FROM busybox
ENV foo=bar
COPY foo /
RUN echo bar > bar
`), []byte(`
FROM nanoserver:latest
USER ContainerAdministrator
ENV foo=bar
COPY foo /
RUN echo bar> bar
`))

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile("foo", []byte("foobar"), 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	target := "example.com/moby/dockerfileexpids:test"
	cacheTarget := registry + "/test/dockerfileexpids:cache"
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
		CacheExports: []client.CacheOptionsEntry{
			{
				Type:  "registry",
				Attrs: map[string]string{"ref": cacheTarget},
			},
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}

	ctd, err := newContainerd(cdAddress)
	require.NoError(t, err)
	defer ctd.Close()

	ctx := namespaces.WithNamespace(sb.Context(), "buildkit")

	_, err = f.Solve(sb.Context(), c, opt, nil)
	require.NoError(t, err)

	img, err := ctd.ImageService().Get(ctx, target)
	require.NoError(t, err)

	err = ctd.ImageService().Delete(ctx, target)
	require.NoError(t, err)

	ensurePruneAll(t, c, sb)

	target2 := "example.com/moby/dockerfileexpids2:test"

	opt.Exports[0].Attrs["name"] = target2
	opt.FrontendAttrs["cache-from"] = cacheTarget

	_, err = f.Solve(sb.Context(), c, opt, nil)
	require.NoError(t, err)

	img2, err := ctd.ImageService().Get(ctx, target2)
	require.NoError(t, err)

	require.Equal(t, img.Target, img2.Target)
}

func testNoCache(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := integration.UnixOrWindows([]byte(`
FROM busybox AS s0
RUN cat /dev/urandom | head -c 100 | sha256sum | tee unique
FROM busybox AS s1
RUN cat /dev/urandom | head -c 100 | sha256sum | tee unique2
FROM scratch
COPY --from=s0 unique /
COPY --from=s1 unique2 /
`), []byte(`
FROM nanoserver:latest AS s0
USER ContainerAdministrator
RUN echo %RANDOM%%RANDOM%%RANDOM%> unique
FROM nanoserver:latest AS s1
USER ContainerAdministrator
RUN echo %RANDOM%%RANDOM%%RANDOM%> unique2
FROM nanoserver:latest
COPY --from=s0 unique /
COPY --from=s1 unique2 /
`))
	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	destDir := t.TempDir()

	opt := client.SolveOpt{
		FrontendAttrs: map[string]string{},
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

	destDir2 := t.TempDir()

	opt.FrontendAttrs["no-cache"] = ""
	opt.Exports[0].OutputDir = destDir2

	_, err = f.Solve(sb.Context(), c, opt, nil)
	require.NoError(t, err)

	unique1Dir1, err := os.ReadFile(filepath.Join(destDir, "unique"))
	require.NoError(t, err)

	unique1Dir2, err := os.ReadFile(filepath.Join(destDir2, "unique"))
	require.NoError(t, err)

	unique2Dir1, err := os.ReadFile(filepath.Join(destDir, "unique2"))
	require.NoError(t, err)

	unique2Dir2, err := os.ReadFile(filepath.Join(destDir2, "unique2"))
	require.NoError(t, err)

	require.NotEqual(t, string(unique1Dir1), string(unique1Dir2))
	require.NotEqual(t, string(unique2Dir1), string(unique2Dir2))

	destDir3 := t.TempDir()

	opt.FrontendAttrs["no-cache"] = "s1"
	opt.Exports[0].OutputDir = destDir3

	_, err = f.Solve(sb.Context(), c, opt, nil)
	require.NoError(t, err)

	unique1Dir3, err := os.ReadFile(filepath.Join(destDir3, "unique"))
	require.NoError(t, err)

	unique2Dir3, err := os.ReadFile(filepath.Join(destDir3, "unique2"))
	require.NoError(t, err)

	require.Equal(t, string(unique1Dir2), string(unique1Dir3))
	require.NotEqual(t, string(unique2Dir1), string(unique2Dir3))
}

// moby/buildkit#5305
func testCacheMountModeNoCache(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM busybox AS base
ARG FOO=abc
RUN --mount=type=cache,target=/cache,mode=0773 touch /cache/$FOO && ls -l /cache | wc -l > /out

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

	opt := client.SolveOpt{
		FrontendAttrs: map[string]string{},
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

	opt.FrontendAttrs["no-cache"] = ""

	dt, err := os.ReadFile(filepath.Join(destDir, "out"))
	require.NoError(t, err)
	require.Equal(t, "2\n", string(dt))

	opt.FrontendAttrs["build-arg:FOO"] = "def"

	_, err = f.Solve(sb.Context(), c, opt, nil)
	require.NoError(t, err)

	dt, err = os.ReadFile(filepath.Join(destDir, "out"))
	require.NoError(t, err)
	require.Equal(t, "2\n", string(dt))

	// safety check without no-cache
	delete(opt.FrontendAttrs, "no-cache")
	opt.FrontendAttrs["build-arg:FOO"] = "ghi"

	_, err = f.Solve(sb.Context(), c, opt, nil)
	require.NoError(t, err)

	dt, err = os.ReadFile(filepath.Join(destDir, "out"))
	require.NoError(t, err)
	require.Equal(t, "3\n", string(dt))
}

// #2008
func testWildcardRenameCache(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM alpine
COPY file* /files/
RUN ls /files/file1
`,
		`
FROM nanoserver
COPY file* /
RUN dir file1
`,
	))
	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile("file1", []byte("foo"), 0600),
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

	err = os.Rename(filepath.Join(dir.Name, "file1"), filepath.Join(dir.Name, "file2"))
	require.NoError(t, err)

	// cache should be invalidated and build should fail
	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.Error(t, err)
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
