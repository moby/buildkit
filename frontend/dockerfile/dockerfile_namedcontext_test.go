package dockerfile

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/plugins/content/local"
	"github.com/containerd/continuity/fs/fstest"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/frontend/dockerui"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/filesync"
	"github.com/moby/buildkit/solver/pb"
	spb "github.com/moby/buildkit/sourcepolicy/pb"
	"github.com/moby/buildkit/util/contentutil"
	"github.com/moby/buildkit/util/testutil"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/moby/buildkit/util/testutil/workers"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tonistiigi/fsutil"
	"golang.org/x/sync/errgroup"
)

func init() {
	allTests = append(allTests, integration.TestFuncs(
		testNamedImageContext,
		testNamedImageContextPlatform,
		testNamedImageContextTimestamps,
		testNamedImageContextScratch,
		testNamedLocalContext,
		testNamedOCILayoutContext,
		testNamedOCILayoutContextExport,
		testNamedInputContext,
		testNamedMultiplatformInputContext,
		testNamedFilteredContext,
		testSourcePolicyWithNamedContext,
		testEagerNamedContextLookup,
		testLocalCustomSessionID,
	)...)
}

func testNamedImageContext(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	ctx := sb.Context()

	c, err := client.New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	dockerfile := []byte(`
FROM busybox AS base
RUN cat /etc/alpine-release > /out
FROM scratch
COPY --from=base /out /
`)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	f := getFrontend(t, sb)

	destDir := t.TempDir()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			// Make sure image resolution works as expected, do not add a tag or locator.
			"context:busybox": "docker-image://alpine",
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

	dt, err := os.ReadFile(filepath.Join(destDir, "out"))
	require.NoError(t, err)
	require.Greater(t, len(dt), 0)

	workers.CheckFeatureCompat(t, sb, workers.FeatureDirectPush)

	// Now test with an image with custom envs
	dockerfile = []byte(`
FROM alpine:latest
ENV PATH=/foobar:$PATH
ENV FOOBAR=foobar
`)

	dir = integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)
	target := registry + "/buildkit/testnamedimagecontext:latest"

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
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

	dockerfile = []byte(`
FROM busybox AS base
RUN cat /etc/alpine-release > /out
RUN env | grep PATH > /env_path
RUN env | grep FOOBAR > /env_foobar
FROM scratch
COPY --from=base /out /
COPY --from=base /env_path /
COPY --from=base /env_foobar /
	`)

	dir = integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	f = getFrontend(t, sb)

	destDir = t.TempDir()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"context:busybox": "docker-image://" + target,
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

	dt, err = os.ReadFile(filepath.Join(destDir, "out"))
	require.NoError(t, err)
	require.Greater(t, len(dt), 0)

	dt, err = os.ReadFile(filepath.Join(destDir, "env_foobar"))
	require.NoError(t, err)
	require.Equal(t, "FOOBAR=foobar", strings.TrimSpace(string(dt)))

	dt, err = os.ReadFile(filepath.Join(destDir, "env_path"))
	require.NoError(t, err)
	require.Contains(t, string(dt), "/foobar:")

	// this case checks replacing stage that is based on another stage.
	// moby/buildkit#5578-2539397486

	dockerfile = []byte(`
FROM busybox AS parent
FROM parent AS base
RUN echo base > /out
FROM base
RUN [ -f /etc/alpine-release ]
RUN [ ! -f /out ]
`)

	dir = integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	f = getFrontend(t, sb)

	destDir = t.TempDir()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"context:base": "docker-image://" + target,
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
}

func testNamedImageContextPlatform(t *testing.T, sb integration.Sandbox) {
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

	// Build a base image and force buildkit to generate a manifest list.
	baseImage := integration.UnixOrWindows(
		"alpine",
		"nanoserver",
	)

	dockerfile := fmt.Appendf(nil, `FROM --platform=$BUILDPLATFORM %s:latest`, baseImage)

	target := registry + "/buildkit/testnamedimagecontextplatform:latest"

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	f := getFrontend(t, sb)

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"build-arg:BUILDKIT_MULTI_PLATFORM": "true",
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
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
		FROM --platform=$BUILDPLATFORM %s AS target
		RUN echo hello
		`, baseImage)

	dir = integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	f = getFrontend(t, sb)

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"context:busybox": "docker-image://" + target,
			// random platform that would never exist so it doesn't conflict with the build machine
			// here we specifically want to make sure that the platform chosen for the image source is the one in the dockerfile not the target platform.
			"platform": "darwin/ppc64le",
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)
}

func testNamedImageContextTimestamps(t *testing.T, sb integration.Sandbox) {
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
FROM alpine
RUN echo foo >> /test
`)
	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	target := registry + "/buildkit/testnamedimagecontexttimestamps:latest"
	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
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

	desc, provider, err := contentutil.ProviderFromRef(target)
	require.NoError(t, err)
	img, err := testutil.ReadImage(sb.Context(), provider, desc)
	require.NoError(t, err)

	dirDerived := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	targetDerived := registry + "/buildkit/testnamedimagecontexttimestampsderived:latest"
	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"context:alpine": "docker-image://" + target,
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dirDerived,
			dockerui.DefaultLocalNameContext:    dirDerived,
		},
		Exports: []client.ExportEntry{
			{
				Type: client.ExporterImage,
				Attrs: map[string]string{
					"name": targetDerived,
					"push": "true",
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	desc, provider, err = contentutil.ProviderFromRef(targetDerived)
	require.NoError(t, err)
	imgDerived, err := testutil.ReadImage(sb.Context(), provider, desc)
	require.NoError(t, err)

	require.NotEqual(t, img.Img.Created, imgDerived.Img.Created)
	diff := imgDerived.Img.Created.Sub(*img.Img.Created)
	require.Greater(t, diff, time.Duration(0))
	require.Less(t, diff, 10*time.Minute)
}

func testNamedImageContextScratch(t *testing.T, sb integration.Sandbox) {
	ctx := sb.Context()

	c, err := client.New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	dockerfile := fmt.Appendf(nil,
		`	
FROM %s AS build
COPY <<EOF /out
hello world!
EOF
`,
		integration.UnixOrWindows("busybox", "nanoserver"))

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	f := getFrontend(t, sb)

	destDir := t.TempDir()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"context:busybox": "docker-image://scratch",
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

	items, err := os.ReadDir(destDir)

	fileNames := []string{}

	for _, item := range items {
		if item.Name() == "out" {
			fileNames = append(fileNames, item.Name())
		}
	}

	require.NoError(t, err)
	require.Equal(t, 1, len(fileNames))
	require.Equal(t, "out", fileNames[0])

	dt, err := os.ReadFile(filepath.Join(destDir, "out"))
	require.NoError(t, err)
	require.Equal(t, "hello world!\n", string(dt))
}

func testNamedLocalContext(t *testing.T, sb integration.Sandbox) {
	ctx := sb.Context()

	c, err := client.New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM busybox AS base
RUN cat /etc/alpine-release > /out
FROM scratch
COPY --from=base /o* /
`,
		`
FROM nanoserver AS base
RUN type License.txt > /out
FROM nanoserver
COPY --from=base /o* /
`,
	))

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	outf := []byte(`dummy-result`)

	dir2 := integration.Tmpdir(
		t,
		fstest.CreateFile("out", outf, 0600),
		fstest.CreateFile("out2", outf, 0600),
		fstest.CreateFile(".dockerignore", []byte("out2\n"), 0600),
	)

	f := getFrontend(t, sb)

	destDir := t.TempDir()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"context:base": "local:basedir",
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
			"basedir":                           dir2,
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
	require.Greater(t, len(dt), 0)

	_, err = os.ReadFile(filepath.Join(destDir, "out2"))
	require.Error(t, err)
	require.True(t, errors.Is(err, os.ErrNotExist))
}

func testNamedOCILayoutContext(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureOCIExporter, workers.FeatureOCILayout)
	// how this test works:
	// 1- we use a regular builder with a dockerfile to create an image two files: "out" with content "first", "out2" with content "second"
	// 2- we save the output to an OCI layout dir
	// 3- we use another regular builder with a dockerfile to build using a referenced context "base", but override it to reference the output of the previous build
	// 4- we check that the output of the second build matches our OCI layout, and not the referenced image
	ctx := sb.Context()

	c, err := client.New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	// create a tempdir where we will store the OCI layout
	ocidir := t.TempDir()

	ociDockerfile := []byte(integration.UnixOrWindows(
		`
	FROM busybox:latest
	WORKDIR /test
	RUN sh -c "echo -n first > out"
	RUN sh -c "echo -n second > out2"
	ENV foo=bar
	`,
		`
	FROM nanoserver
	WORKDIR /test
	RUN echo first> out"
	RUN echo second> out2"
	ENV foo=bar
	`,
	))
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

	// we will use this simple dockerfile to test
	// 1. busybox is used as is, but because we override the context for base,
	//    when we run `COPY --from=base`, it should take the /o* from the image in the store,
	//    rather than what we built on the first 2 lines here.
	// 2. we override the context for `foo` to be our local OCI store, which has an `ENV foo=bar` override.
	//    As such, the `RUN echo $foo` step should have `$foo` set to `"bar"`, and so
	//    when we `COPY --from=imported`, it should have the content of `/outfoo` as `"bar"`
	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM busybox AS base
RUN cat /etc/alpine-release > out

FROM foo AS imported
RUN echo -n $foo > outfoo

FROM scratch
COPY --from=base /test/o* /
COPY --from=imported /test/outfoo /
`,
		`
FROM nanoserver AS base
USER ContainerAdministrator
RUN ver > out

FROM foo AS imported
RUN echo %foo%> outfoo

FROM nanoserver
COPY --from=base /test/o* /
COPY --from=imported /test/outfoo /
`,
	))

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	destDir := t.TempDir()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"context:base": fmt.Sprintf("oci-layout:%s@sha256:%s", ociID, digest),
			"context:foo":  fmt.Sprintf("oci-layout:%s@sha256:%s", ociID, digest),
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

	// echo for Windows adds a \n
	newLine := integration.UnixOrWindows("", "\r\n")

	dt, err := os.ReadFile(filepath.Join(destDir, "out"))
	require.NoError(t, err)
	require.Greater(t, len(dt), 0)
	require.Equal(t, []byte("first"+newLine), dt)

	dt, err = os.ReadFile(filepath.Join(destDir, "out2"))
	require.NoError(t, err)
	require.Greater(t, len(dt), 0)
	require.Equal(t, []byte("second"+newLine), dt)

	dt, err = os.ReadFile(filepath.Join(destDir, "outfoo"))
	require.NoError(t, err)
	require.Greater(t, len(dt), 0)
	require.Equal(t, []byte("bar"+newLine), dt)
}

func testNamedOCILayoutContextExport(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureOCIExporter, workers.FeatureOCILayout)
	ctx := sb.Context()

	c, err := client.New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	ocidir := t.TempDir()

	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM scratch
WORKDIR /test
ENV foo=bar
	`,
		`
FROM nanoserver
WORKDIR /test
ENV foo=bar
	`,
	))
	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	f := getFrontend(t, sb)

	outW := bytes.NewBuffer(nil)
	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
		Exports: []client.ExportEntry{{
			Type:   client.ExporterOCI,
			Output: fixedWriteCloser(nopWriteCloser{outW}),
		}},
	}, nil)
	require.NoError(t, err)

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

	dockerfile = []byte(`
FROM nonexistent AS base
`)

	dir = integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	outW = bytes.NewBuffer(nil)
	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"context:nonexistent": fmt.Sprintf("oci-layout:%s@sha256:%s", ociID, digest),
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
				Type:   client.ExporterOCI,
				Output: fixedWriteCloser(nopWriteCloser{outW}),
			},
		},
	}, nil)
	require.NoError(t, err)

	m, err = testutil.ReadTarToMap(outW.Bytes(), false)
	require.NoError(t, err)

	err = json.Unmarshal(m[ocispecs.ImageIndexFile].Data, &index)
	require.NoError(t, err)
	require.Equal(t, 1, len(index.Manifests))
	digest = index.Manifests[0].Digest.Hex()

	var mfst ocispecs.Manifest
	require.NoError(t, json.Unmarshal(m[ocispecs.ImageBlobsDir+"/sha256/"+digest].Data, &mfst))
	digest = mfst.Config.Digest.Hex()

	var cfg ocispecs.Image
	require.NoError(t, json.Unmarshal(m[ocispecs.ImageBlobsDir+"/sha256/"+digest].Data, &cfg))

	wd := integration.UnixOrWindows("/test", "\\test")
	require.Equal(t, wd, cfg.Config.WorkingDir)
	require.Contains(t, cfg.Config.Env, "foo=bar")
}

func testNamedInputContext(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	ctx := sb.Context()

	c, err := client.New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	dockerfile := []byte(`
FROM alpine
ENV FOO=bar
RUN echo first > /out
`)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	dockerfile2 := []byte(`
FROM base AS build
RUN echo "foo is $FOO" > /foo
FROM scratch
COPY --from=build /foo /out /
`)

	dir2 := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile2, 0600),
	)

	f := getFrontend(t, sb)

	b := func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		res, err := f.SolveGateway(ctx, c, gateway.SolveRequest{})
		if err != nil {
			return nil, err
		}
		ref, err := res.SingleRef()
		if err != nil {
			return nil, err
		}
		st, err := ref.ToState()
		if err != nil {
			return nil, err
		}

		def, err := st.Marshal(ctx)
		if err != nil {
			return nil, err
		}

		dt, ok := res.Metadata["containerimage.config"]
		if !ok {
			return nil, errors.Errorf("no containerimage.config in metadata")
		}

		dt, err = json.Marshal(map[string][]byte{
			"containerimage.config": dt,
		})
		if err != nil {
			return nil, err
		}

		res, err = f.SolveGateway(ctx, c, gateway.SolveRequest{
			FrontendOpt: map[string]string{
				"dockerfilekey":       dockerui.DefaultLocalNameDockerfile + "2",
				"context:base":        "input:base",
				"input-metadata:base": string(dt),
			},
			FrontendInputs: map[string]*pb.Definition{
				"base": def.ToPB(),
			},
		})
		if err != nil {
			return nil, err
		}
		return res, nil
	}

	product := "buildkit_test"

	destDir := t.TempDir()

	_, err = c.Build(ctx, client.SolveOpt{
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile:       dir,
			dockerui.DefaultLocalNameContext:          dir,
			dockerui.DefaultLocalNameDockerfile + "2": dir2,
		},
		Exports: []client.ExportEntry{
			{
				Type:      client.ExporterLocal,
				OutputDir: destDir,
			},
		},
	}, product, b, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "out"))
	require.NoError(t, err)
	require.Equal(t, "first\n", string(dt))

	dt, err = os.ReadFile(filepath.Join(destDir, "foo"))
	require.NoError(t, err)
	require.Equal(t, "foo is bar\n", string(dt))
}

func testNamedMultiplatformInputContext(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	workers.CheckFeatureCompat(t, sb, workers.FeatureMultiPlatform)
	ctx := sb.Context()

	c, err := client.New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	dockerfile := []byte(`
FROM --platform=$BUILDPLATFORM alpine
ARG TARGETARCH
ENV FOO=bar-$TARGETARCH
RUN echo "foo $TARGETARCH" > /out
`)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	dockerfile2 := []byte(`
FROM base AS build
RUN echo "foo is $FOO" > /foo
FROM scratch
COPY --from=build /foo /out /
`)

	dir2 := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile2, 0600),
	)

	f := getFrontend(t, sb)

	b := func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		res, err := f.SolveGateway(ctx, c, gateway.SolveRequest{
			FrontendOpt: map[string]string{
				"platform": "linux/amd64,linux/arm64",
			},
		})
		if err != nil {
			return nil, err
		}

		if len(res.Refs) != 2 {
			return nil, errors.Errorf("expected 2 refs, got %d", len(res.Refs))
		}

		inputs := map[string]*pb.Definition{}
		st, err := res.Refs["linux/amd64"].ToState()
		if err != nil {
			return nil, err
		}
		def, err := st.Marshal(ctx)
		if err != nil {
			return nil, err
		}
		inputs["base::linux/amd64"] = def.ToPB()

		st, err = res.Refs["linux/arm64"].ToState()
		if err != nil {
			return nil, err
		}
		def, err = st.Marshal(ctx)
		if err != nil {
			return nil, err
		}
		inputs["base::linux/arm64"] = def.ToPB()

		frontendOpt := map[string]string{
			"dockerfilekey":             dockerui.DefaultLocalNameDockerfile + "2",
			"context:base::linux/amd64": "input:base::linux/amd64",
			"context:base::linux/arm64": "input:base::linux/arm64",
			"platform":                  "linux/amd64,linux/arm64",
		}

		dt, ok := res.Metadata["containerimage.config/linux/amd64"]
		if !ok {
			return nil, errors.Errorf("no containerimage.config in metadata")
		}
		dt, err = json.Marshal(map[string][]byte{
			"containerimage.config": dt,
		})
		if err != nil {
			return nil, err
		}
		frontendOpt["input-metadata:base::linux/amd64"] = string(dt)

		dt, ok = res.Metadata["containerimage.config/linux/arm64"]
		if !ok {
			return nil, errors.Errorf("no containerimage.config in metadata")
		}
		dt, err = json.Marshal(map[string][]byte{
			"containerimage.config": dt,
		})
		if err != nil {
			return nil, err
		}
		frontendOpt["input-metadata:base::linux/arm64"] = string(dt)

		res, err = f.SolveGateway(ctx, c, gateway.SolveRequest{
			FrontendOpt:    frontendOpt,
			FrontendInputs: inputs,
		})
		if err != nil {
			return nil, err
		}
		return res, nil
	}

	product := "buildkit_test"

	destDir := t.TempDir()

	_, err = c.Build(ctx, client.SolveOpt{
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile:       dir,
			dockerui.DefaultLocalNameContext:          dir,
			dockerui.DefaultLocalNameDockerfile + "2": dir2,
		},
		Exports: []client.ExportEntry{
			{
				Type:      client.ExporterLocal,
				OutputDir: destDir,
			},
		},
	}, product, b, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "linux_amd64/out"))
	require.NoError(t, err)
	require.Equal(t, "foo amd64\n", string(dt))

	dt, err = os.ReadFile(filepath.Join(destDir, "linux_amd64/foo"))
	require.NoError(t, err)
	require.Equal(t, "foo is bar-amd64\n", string(dt))

	dt, err = os.ReadFile(filepath.Join(destDir, "linux_arm64/out"))
	require.NoError(t, err)
	require.Equal(t, "foo arm64\n", string(dt))

	dt, err = os.ReadFile(filepath.Join(destDir, "linux_arm64/foo"))
	require.NoError(t, err)
	require.Equal(t, "foo is bar-arm64\n", string(dt))
}

func testNamedFilteredContext(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	ctx := sb.Context()

	c, err := client.New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	fooDir := integration.Tmpdir(t,
		// small file
		fstest.CreateFile("foo", []byte(`foo`), 0600),
		// blank file that's just large
		fstest.CreateFile("bar", make([]byte, 4096*1000), 0600),
	)

	f := getFrontend(t, sb)

	runTest := func(t *testing.T, dockerfile []byte, target string, min, max int64) {
		t.Run(target, func(t *testing.T) {
			dir := integration.Tmpdir(
				t,
				fstest.CreateFile(dockerui.DefaultDockerfileName, dockerfile, 0600),
			)

			ch := make(chan *client.SolveStatus)

			eg, ctx := errgroup.WithContext(sb.Context())
			eg.Go(func() error {
				_, err := f.Solve(ctx, c, client.SolveOpt{
					FrontendAttrs: map[string]string{
						"context:foo": "local:foo",
						"target":      target,
					},
					LocalMounts: map[string]fsutil.FS{
						dockerui.DefaultLocalNameDockerfile: dir,
						dockerui.DefaultLocalNameContext:    dir,
						"foo":                               fooDir,
					},
				}, ch)
				return err
			})

			eg.Go(func() error {
				transferred := make(map[string]int64)
				re := regexp.MustCompile(`transferring (.+):`)
				for ss := range ch {
					for _, status := range ss.Statuses {
						m := re.FindStringSubmatch(status.ID)
						if m == nil {
							continue
						}

						ctxName := m[1]
						transferred[ctxName] = status.Current
					}
				}

				if foo := transferred["foo"]; foo < min {
					return errors.Errorf("not enough data was transferred, %d < %d", foo, min)
				} else if foo > max {
					return errors.Errorf("too much data was transferred, %d > %d", foo, max)
				}
				return nil
			})

			err := eg.Wait()
			require.NoError(t, err)
		})
	}

	dockerfileBase := []byte(`
FROM scratch AS copy_from
COPY --from=foo /foo /

FROM alpine AS run_mount
RUN --mount=from=foo,src=/foo,target=/in/foo cp /in/foo /foo

FROM foo AS image_source
COPY --from=alpine / /
RUN cat /foo > /bar

FROM scratch AS all
COPY --link --from=copy_from /foo /foo.b
COPY --link --from=run_mount /foo /foo.c
COPY --link --from=image_source /bar /foo.d
`)

	t.Run("new", func(t *testing.T) {
		runTest(t, dockerfileBase, "run_mount", 1, 1024)
		runTest(t, dockerfileBase, "copy_from", 1, 1024)
		runTest(t, dockerfileBase, "image_source", 4096*1000, math.MaxInt64)
		runTest(t, dockerfileBase, "all", 4096*1000, math.MaxInt64)
	})

	dockerfileFull := append([]byte(`
FROM scratch AS foo
COPY <<EOF /foo
test
EOF
`), dockerfileBase...)

	t.Run("replace", func(t *testing.T) {
		runTest(t, dockerfileFull, "run_mount", 1, 1024)
		runTest(t, dockerfileFull, "copy_from", 1, 1024)
		runTest(t, dockerfileFull, "image_source", 4096*1000, math.MaxInt64)
		runTest(t, dockerfileFull, "all", 4096*1000, math.MaxInt64)
	})
}

func testSourcePolicyWithNamedContext(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)
	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	dockerfile := []byte(integration.UnixOrWindows(
		`
	FROM scratch AS replace

	FROM scratch
	COPY --from=replace /foo /
	`,
		`
	FROM nanoserver AS replace

	FROM nanoserver
	COPY --from=replace /foo /
	`,
	))

	replaceContext := integration.Tmpdir(t, fstest.CreateFile("foo", []byte("foo"), 0644))
	mainContext := integration.Tmpdir(t, fstest.CreateFile("Dockerfile", dockerfile, 0600))

	out := t.TempDir()
	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		Exports: []client.ExportEntry{
			{Type: client.ExporterLocal, OutputDir: out},
		},
		FrontendAttrs: map[string]string{
			"context:replace": integration.UnixOrWindows(
				"docker-image:docker.io/library/alpine:latest",
				"docker-image:docker.io/library/nanoserver:plus",
			),
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: mainContext,
			dockerui.DefaultLocalNameContext:    mainContext,
			"test":                              replaceContext,
		},
		SourcePolicy: &spb.Policy{
			Rules: []*spb.Rule{
				{
					Action: spb.PolicyAction_CONVERT,
					Selector: &spb.Selector{
						Identifier: integration.UnixOrWindows(
							"docker-image://docker.io/library/alpine:latest",
							"docker-image://docker.io/library/nanoserver:plus",
						),
					},
					Updates: &spb.Update{
						Identifier: "local://test",
					},
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(out, "foo"))
	require.NoError(t, err)
	require.Equal(t, "foo", string(dt))
}

// testEagerNamedContextLookup tests that named context are not loaded if
// they are not used by current build.
func testEagerNamedContextLookup(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")

	f := getFrontend(t, sb)
	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	dockerfile := []byte(`
FROM broken AS notused

FROM alpine AS base
RUN echo "base" > /foo

FROM busybox AS otherstage

FROM scratch
COPY --from=base /foo /foo
	`)

	dir := integration.Tmpdir(t, fstest.CreateFile("Dockerfile", dockerfile, 0600))

	out := t.TempDir()
	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		Exports: []client.ExportEntry{
			{Type: client.ExporterLocal, OutputDir: out},
		},
		FrontendAttrs: map[string]string{
			"context:notused": "docker-image://docker.io/library/nosuchimage:latest",
			"context:busybox": "docker-image://docker.io/library/dontexist:latest",
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(out, "foo"))
	require.NoError(t, err)
	require.Equal(t, "base\n", string(dt))
}

func testLocalCustomSessionID(t *testing.T, sb integration.Sandbox) {
	ctx := sb.Context()

	c, err := client.New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM scratch AS base
FROM scratch
COPY out /out1
COPY --from=base /another /out2
`,
		`
FROM nanoserver AS base
FROM nanoserver
COPY out /out1
COPY --from=base /another /out2
`,
	))

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	dir2 := integration.Tmpdir(
		t,
		fstest.CreateFile("out", []byte("contents1"), 0600),
	)

	dir3 := integration.Tmpdir(
		t,
		fstest.CreateFile("another", []byte("contents2"), 0600),
	)

	f := getFrontend(t, sb)

	destDir := t.TempDir()

	dirs := filesync.NewFSSyncProvider(filesync.StaticDirSource{
		dockerui.DefaultLocalNameDockerfile: dir,
		dockerui.DefaultLocalNameContext:    dir2,
		"basedir":                           dir3,
	})

	s, err := session.NewSession(ctx, "hint")
	require.NoError(t, err)
	s.Allow(dirs)
	go func() {
		err := s.Run(ctx, c.Dialer())
		assert.NoError(t, err)
	}()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"context:base": "local:basedir",
			"local-sessionid:" + dockerui.DefaultLocalNameDockerfile: s.ID(),
			"local-sessionid:" + dockerui.DefaultLocalNameContext:    s.ID(),
			"local-sessionid:basedir":                                s.ID(),
		},
		Exports: []client.ExportEntry{
			{
				Type:      client.ExporterLocal,
				OutputDir: destDir,
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "out1"))
	require.NoError(t, err)
	require.Equal(t, "contents1", string(dt))

	dt, err = os.ReadFile(filepath.Join(destDir, "out2"))
	require.NoError(t, err)
	require.Equal(t, "contents2", string(dt))
}
