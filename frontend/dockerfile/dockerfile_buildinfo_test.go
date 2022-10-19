package dockerfile

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/containerd/continuity/fs/fstest"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	"github.com/moby/buildkit/frontend/dockerfile/builder"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/solver/pb"
	binfotypes "github.com/moby/buildkit/util/buildinfo/types"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var buildinfoTests = integration.TestFuncs(
	testBuildInfoSources,
	testBuildInfoSourcesNoop,
	testBuildInfoAttrs,
	testBuildInfoMultiPlatform,
	testBuildInfoImageContext,
	testBuildInfoLocalContext,
	testBuildInfoDeps,
	testBuildInfoDepsMultiPlatform,
	testBuildInfoDepsMainNoSource,
)

func init() {
	allTests = append(allTests, buildinfoTests...)
}

// moby/buildkit#2311
func testBuildInfoSources(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)
	f.RequiresBuildctl(t)

	gitDir := t.TempDir()

	dockerfile := `
FROM alpine:latest@sha256:21a3deaa0d32a8057914f36584b5288d2e5ecc984380bc0118285c70fa8c9300 AS alpine
FROM busybox:latest
ADD https://raw.githubusercontent.com/moby/moby/master/README.md /
COPY --from=alpine /bin/busybox /alpine-busybox
`

	err := os.WriteFile(filepath.Join(gitDir, "Dockerfile"), []byte(dockerfile), 0600)
	require.NoError(t, err)

	err = runShell(gitDir,
		"git init",
		"git config --local user.email test",
		"git config --local user.name test",
		"git add Dockerfile",
		"git commit -m initial",
		"git branch buildinfo",
		"git update-server-info",
	)
	require.NoError(t, err)

	server := httptest.NewServer(http.FileServer(http.Dir(filepath.Join(gitDir))))
	defer server.Close()

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	var exports []client.ExportEntry
	if integration.IsTestDockerd() {
		exports = []client.ExportEntry{{
			Type: "moby",
			Attrs: map[string]string{
				"name": "reg.dummy:5000/buildkit/test:latest",
			},
		}}
	} else {
		exports = []client.ExportEntry{{
			Type:   client.ExporterOCI,
			Attrs:  map[string]string{},
			Output: fixedWriteCloser(nopWriteCloser{io.Discard}),
		}}
	}

	res, err := f.Solve(sb.Context(), c, client.SolveOpt{
		Exports: exports,
		FrontendAttrs: map[string]string{
			builder.DefaultLocalNameContext: server.URL + "/.git#buildinfo",
		},
	}, nil)
	require.NoError(t, err)

	require.Contains(t, res.ExporterResponse, exptypes.ExporterBuildInfo)
	dtbi, err := base64.StdEncoding.DecodeString(res.ExporterResponse[exptypes.ExporterBuildInfo])
	require.NoError(t, err)

	var bi binfotypes.BuildInfo
	err = json.Unmarshal(dtbi, &bi)
	require.NoError(t, err)

	require.Contains(t, bi.Attrs, "context")
	require.Equal(t, server.URL+"/.git#buildinfo", *bi.Attrs["context"])

	sources := bi.Sources
	require.Equal(t, 3, len(sources))

	assert.Equal(t, binfotypes.SourceTypeDockerImage, sources[0].Type)
	assert.Equal(t, "docker.io/library/alpine:latest@sha256:21a3deaa0d32a8057914f36584b5288d2e5ecc984380bc0118285c70fa8c9300", sources[0].Ref)
	assert.Equal(t, "sha256:21a3deaa0d32a8057914f36584b5288d2e5ecc984380bc0118285c70fa8c9300", sources[0].Pin)

	assert.Equal(t, binfotypes.SourceTypeDockerImage, sources[1].Type)
	assert.Equal(t, "docker.io/library/busybox:latest", sources[1].Ref)
	assert.NotEmpty(t, sources[1].Pin)

	assert.Equal(t, binfotypes.SourceTypeHTTP, sources[2].Type)
	assert.Equal(t, "https://raw.githubusercontent.com/moby/moby/master/README.md", sources[2].Ref)
	assert.Equal(t, "sha256:419455202b0ef97e480d7f8199b26a721a417818bc0e2d106975f74323f25e6c", sources[2].Pin)
}

func testBuildInfoSourcesNoop(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)
	f.RequiresBuildctl(t)

	dockerfile := `
FROM busybox:latest
`

	dir, err := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", []byte(dockerfile), 0600),
	)
	require.NoError(t, err)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	var exports []client.ExportEntry
	if integration.IsTestDockerd() {
		exports = []client.ExportEntry{{
			Type: "moby",
			Attrs: map[string]string{
				"name": "reg.dummy:5000/buildkit/test:latest",
			},
		}}
	} else {
		exports = []client.ExportEntry{{
			Type:   client.ExporterOCI,
			Attrs:  map[string]string{},
			Output: fixedWriteCloser(nopWriteCloser{io.Discard}),
		}}
	}

	res, err := f.Solve(sb.Context(), c, client.SolveOpt{
		Exports: exports,
		LocalDirs: map[string]string{
			builder.DefaultLocalNameDockerfile: dir,
			builder.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)

	require.Contains(t, res.ExporterResponse, exptypes.ExporterBuildInfo)
	dtbi, err := base64.StdEncoding.DecodeString(res.ExporterResponse[exptypes.ExporterBuildInfo])
	require.NoError(t, err)

	var bi binfotypes.BuildInfo
	err = json.Unmarshal(dtbi, &bi)
	require.NoError(t, err)

	sources := bi.Sources
	require.Equal(t, 1, len(sources))

	assert.Equal(t, binfotypes.SourceTypeDockerImage, sources[0].Type)
	assert.Equal(t, "docker.io/library/busybox:latest", sources[0].Ref)
	assert.NotEmpty(t, sources[0].Pin)
}

// moby/buildkit#2476
func testBuildInfoAttrs(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)
	f.RequiresBuildctl(t)

	dockerfile := `
FROM busybox:latest
ARG foo
RUN echo $foo
`

	dir, err := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", []byte(dockerfile), 0600),
	)
	require.NoError(t, err)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	var exports []client.ExportEntry
	if integration.IsTestDockerd() {
		exports = []client.ExportEntry{{
			Type: "moby",
			Attrs: map[string]string{
				"name": "reg.dummy:5000/buildkit/test:latest",
			},
		}}
	} else {
		exports = []client.ExportEntry{{
			Type:   client.ExporterOCI,
			Attrs:  map[string]string{},
			Output: fixedWriteCloser(nopWriteCloser{io.Discard}),
		}}
	}

	res, err := f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"build-arg:foo": "bar",
		},
		Exports: exports,
		LocalDirs: map[string]string{
			builder.DefaultLocalNameDockerfile: dir,
			builder.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)

	require.Contains(t, res.ExporterResponse, exptypes.ExporterBuildInfo)
	dtbi, err := base64.StdEncoding.DecodeString(res.ExporterResponse[exptypes.ExporterBuildInfo])
	require.NoError(t, err)

	var bi binfotypes.BuildInfo
	err = json.Unmarshal(dtbi, &bi)
	require.NoError(t, err)

	require.Contains(t, bi.Attrs, "build-arg:foo")
	require.Equal(t, "bar", *bi.Attrs["build-arg:foo"])
}

// moby/buildkit#2476
func testBuildInfoMultiPlatform(t *testing.T, sb integration.Sandbox) {
	integration.SkipIfDockerd(t, sb, "multi-platform")
	f := getFrontend(t, sb)
	f.RequiresBuildctl(t)

	dockerfile := `
FROM busybox:latest
ARG foo
RUN echo $foo
ADD https://raw.githubusercontent.com/moby/moby/master/README.md /
`

	dir, err := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", []byte(dockerfile), 0600),
	)
	require.NoError(t, err)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	platforms := []string{"linux/amd64", "linux/arm64"}

	res, err := f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"build-arg:foo": "bar",
			"platform":      strings.Join(platforms, ","),
		},
		Exports: []client.ExportEntry{
			{
				Type:   client.ExporterOCI,
				Output: fixedWriteCloser(nopWriteCloser{io.Discard}),
			},
		},
		LocalDirs: map[string]string{
			builder.DefaultLocalNameDockerfile: dir,
			builder.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)

	for _, platform := range platforms {
		require.Contains(t, res.ExporterResponse, fmt.Sprintf("%s/%s", exptypes.ExporterBuildInfo, platform))
		dtbi, err := base64.StdEncoding.DecodeString(res.ExporterResponse[fmt.Sprintf("%s/%s", exptypes.ExporterBuildInfo, platform)])
		require.NoError(t, err)

		var bi binfotypes.BuildInfo
		err = json.Unmarshal(dtbi, &bi)
		require.NoError(t, err)

		require.Contains(t, bi.Attrs, "build-arg:foo")
		require.Equal(t, "bar", *bi.Attrs["build-arg:foo"])

		sources := bi.Sources
		require.Equal(t, 2, len(sources))

		assert.Equal(t, binfotypes.SourceTypeDockerImage, sources[0].Type)
		assert.Equal(t, "docker.io/library/busybox:latest", sources[0].Ref)
		assert.NotEmpty(t, sources[0].Pin)

		assert.Equal(t, binfotypes.SourceTypeHTTP, sources[1].Type)
		assert.Equal(t, "https://raw.githubusercontent.com/moby/moby/master/README.md", sources[1].Ref)
		assert.Equal(t, "sha256:419455202b0ef97e480d7f8199b26a721a417818bc0e2d106975f74323f25e6c", sources[1].Pin)
	}
}

func testBuildInfoImageContext(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)
	f.RequiresBuildctl(t)

	dockerfile := `
FROM busybox AS base
RUN cat /etc/alpine-release > /out
FROM scratch
COPY --from=base /out /
`

	dir, err := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", []byte(dockerfile), 0600),
	)
	require.NoError(t, err)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	var exports []client.ExportEntry
	if integration.IsTestDockerd() {
		exports = []client.ExportEntry{{
			Type: "moby",
			Attrs: map[string]string{
				"name": "reg.dummy:5000/buildkit/test:latest",
			},
		}}
	} else {
		exports = []client.ExportEntry{{
			Type:   client.ExporterOCI,
			Attrs:  map[string]string{},
			Output: fixedWriteCloser(nopWriteCloser{io.Discard}),
		}}
	}

	res, err := f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"build-arg:foo":   "bar",
			"context:busybox": "docker-image://alpine",
		},
		Exports: exports,
		LocalDirs: map[string]string{
			builder.DefaultLocalNameDockerfile: dir,
			builder.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)

	require.Contains(t, res.ExporterResponse, exptypes.ExporterBuildInfo)
	dtbi, err := base64.StdEncoding.DecodeString(res.ExporterResponse[exptypes.ExporterBuildInfo])
	require.NoError(t, err)

	var bi binfotypes.BuildInfo
	err = json.Unmarshal(dtbi, &bi)
	require.NoError(t, err)

	require.Contains(t, bi.Attrs, "context:busybox")
	require.Equal(t, "docker-image://alpine", *bi.Attrs["context:busybox"])
	require.Contains(t, bi.Attrs, "build-arg:foo")
	require.Equal(t, "bar", *bi.Attrs["build-arg:foo"])

	sources := bi.Sources
	require.Equal(t, 1, len(sources))
	assert.Equal(t, binfotypes.SourceTypeDockerImage, sources[0].Type)
	assert.Equal(t, "docker.io/library/alpine:latest", sources[0].Ref)
	assert.NotEmpty(t, sources[0].Pin)
}

func testBuildInfoLocalContext(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)
	f.RequiresBuildctl(t)

	dockerfile := `
FROM busybox AS base
RUN cat /etc/alpine-release > /out
FROM scratch
COPY --from=base /o* /
`

	dir, err := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", []byte(dockerfile), 0600),
	)
	require.NoError(t, err)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	outf := []byte(`dummy-result`)

	dir2, err := integration.Tmpdir(
		t,
		fstest.CreateFile("out", outf, 0600),
		fstest.CreateFile("out2", outf, 0600),
		fstest.CreateFile(".dockerignore", []byte("out2\n"), 0600),
	)
	require.NoError(t, err)

	var exports []client.ExportEntry
	if integration.IsTestDockerd() {
		exports = []client.ExportEntry{{
			Type: "moby",
			Attrs: map[string]string{
				"name": "reg.dummy:5000/buildkit/test:latest",
			},
		}}
	} else {
		exports = []client.ExportEntry{{
			Type:   client.ExporterOCI,
			Attrs:  map[string]string{},
			Output: fixedWriteCloser(nopWriteCloser{io.Discard}),
		}}
	}

	res, err := f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"build-arg:foo": "bar",
			"context:base":  "local:basedir",
		},
		Exports: exports,
		LocalDirs: map[string]string{
			builder.DefaultLocalNameDockerfile: dir,
			builder.DefaultLocalNameContext:    dir,
			"basedir":                          dir2,
		},
	}, nil)
	require.NoError(t, err)

	require.Contains(t, res.ExporterResponse, exptypes.ExporterBuildInfo)
	dtbi, err := base64.StdEncoding.DecodeString(res.ExporterResponse[exptypes.ExporterBuildInfo])
	require.NoError(t, err)

	var bi binfotypes.BuildInfo
	err = json.Unmarshal(dtbi, &bi)
	require.NoError(t, err)

	require.Contains(t, bi.Attrs, "context:base")
	require.Equal(t, "local:basedir", *bi.Attrs["context:base"])
	require.Contains(t, bi.Attrs, "build-arg:foo")
	require.Equal(t, "bar", *bi.Attrs["build-arg:foo"])

	require.Equal(t, 0, len(bi.Sources))
}

func testBuildInfoDeps(t *testing.T, sb integration.Sandbox) {
	ctx := sb.Context()
	f := getFrontend(t, sb)
	f.RequiresBuildctl(t)

	c, err := client.New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	dockerfile := []byte(`
FROM alpine
ENV FOO=bar
ADD https://raw.githubusercontent.com/moby/moby/master/README.md /
RUN echo first > /out
`)

	dir, err := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)
	require.NoError(t, err)

	dockerfile2 := []byte(`
FROM base AS build
RUN echo "foo is $FOO" > /foo
FROM busybox
COPY --from=build /foo /out /
`)

	dir2, err := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile2, 0600),
	)
	require.NoError(t, err)

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

		dtic, ok := res.Metadata[exptypes.ExporterImageConfigKey]
		if !ok {
			return nil, errors.Errorf("no containerimage.config in metadata")
		}

		dtbi, ok := res.Metadata[exptypes.ExporterBuildInfo]
		if !ok {
			return nil, errors.Errorf("no containerimage.buildinfo in metadata")
		}

		dt, err := json.Marshal(map[string][]byte{
			exptypes.ExporterImageConfigKey: dtic,
			exptypes.ExporterBuildInfo:      dtbi,
		})
		if err != nil {
			return nil, err
		}

		res, err = f.SolveGateway(ctx, c, gateway.SolveRequest{
			FrontendOpt: map[string]string{
				"dockerfilekey":       builder.DefaultLocalNameDockerfile + "2",
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

	destDir := t.TempDir()

	res, err := c.Build(ctx, client.SolveOpt{
		LocalDirs: map[string]string{
			builder.DefaultLocalNameDockerfile:       dir,
			builder.DefaultLocalNameContext:          dir,
			builder.DefaultLocalNameDockerfile + "2": dir2,
		},
		Exports: []client.ExportEntry{
			{
				Type:      client.ExporterLocal,
				OutputDir: destDir,
			},
		},
	}, "", b, nil)
	require.NoError(t, err)

	require.Contains(t, res.ExporterResponse, exptypes.ExporterBuildInfo)
	dtbi, err := base64.StdEncoding.DecodeString(res.ExporterResponse[exptypes.ExporterBuildInfo])
	require.NoError(t, err)

	var bi binfotypes.BuildInfo
	err = json.Unmarshal(dtbi, &bi)
	require.NoError(t, err)

	require.Contains(t, bi.Attrs, "context:base")
	require.Equal(t, "input:base", *bi.Attrs["context:base"])

	require.Equal(t, 2, len(bi.Sources))

	assert.Equal(t, binfotypes.SourceTypeDockerImage, bi.Sources[0].Type)
	assert.Equal(t, "docker.io/library/busybox:latest", bi.Sources[0].Ref)
	assert.NotEmpty(t, bi.Sources[0].Pin)

	assert.Equal(t, binfotypes.SourceTypeHTTP, bi.Sources[1].Type)
	assert.Equal(t, "https://raw.githubusercontent.com/moby/moby/master/README.md", bi.Sources[1].Ref)
	assert.Equal(t, "sha256:419455202b0ef97e480d7f8199b26a721a417818bc0e2d106975f74323f25e6c", bi.Sources[1].Pin)

	require.Contains(t, bi.Deps, "base")
	depsrc := bi.Deps["base"].Sources
	require.Equal(t, 1, len(depsrc))
	assert.Equal(t, binfotypes.SourceTypeDockerImage, depsrc[0].Type)
	assert.Equal(t, "docker.io/library/alpine:latest", depsrc[0].Ref)
	assert.NotEmpty(t, depsrc[0].Pin)
}

func testBuildInfoDepsMultiPlatform(t *testing.T, sb integration.Sandbox) {
	ctx := sb.Context()
	f := getFrontend(t, sb)
	f.RequiresBuildctl(t)

	platforms := []string{"linux/amd64", "linux/arm64"}

	c, err := client.New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	dockerfile := []byte(`
FROM --platform=$BUILDPLATFORM alpine
ARG TARGETARCH
ENV FOO=bar-$TARGETARCH
RUN echo "foo $TARGETARCH" > /out
`)

	dir, err := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)
	require.NoError(t, err)

	dockerfile2 := []byte(`
FROM base AS build
RUN echo "foo is $FOO" > /foo
FROM busybox
COPY --from=build /foo /out /
`)

	dir2, err := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile2, 0600),
	)
	require.NoError(t, err)

	b := func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		res, err := f.SolveGateway(ctx, c, gateway.SolveRequest{
			FrontendOpt: map[string]string{
				"platform": strings.Join(platforms, ","),
			},
		})
		if err != nil {
			return nil, err
		}

		if len(res.Refs) != 2 {
			return nil, errors.Errorf("expected 2 refs, got %d", len(res.Refs))
		}

		frontendOpt := map[string]string{
			"dockerfilekey": builder.DefaultLocalNameDockerfile + "2",
			"platform":      strings.Join(platforms, ","),
		}

		inputs := map[string]*pb.Definition{}
		for _, platform := range platforms {
			frontendOpt["context:base::"+platform] = "input:base::" + platform

			st, err := res.Refs[platform].ToState()
			if err != nil {
				return nil, err
			}
			def, err := st.Marshal(ctx)
			if err != nil {
				return nil, err
			}
			inputs["base::"+platform] = def.ToPB()

			dtic, ok := res.Metadata[exptypes.ExporterImageConfigKey+"/"+platform]
			if !ok {
				return nil, errors.Errorf("no containerimage.config/" + platform + " in metadata")
			}
			dtbi, ok := res.Metadata[exptypes.ExporterBuildInfo+"/"+platform]
			if !ok {
				return nil, errors.Errorf("no containerimage.buildinfo/" + platform + " in metadata")
			}
			dt, err := json.Marshal(map[string][]byte{
				exptypes.ExporterImageConfigKey: dtic,
				exptypes.ExporterBuildInfo:      dtbi,
			})
			if err != nil {
				return nil, err
			}
			frontendOpt["input-metadata:base::"+platform] = string(dt)
		}

		res, err = f.SolveGateway(ctx, c, gateway.SolveRequest{
			FrontendOpt:    frontendOpt,
			FrontendInputs: inputs,
		})
		if err != nil {
			return nil, err
		}
		return res, nil
	}

	destDir := t.TempDir()

	res, err := c.Build(ctx, client.SolveOpt{
		LocalDirs: map[string]string{
			builder.DefaultLocalNameDockerfile:       dir,
			builder.DefaultLocalNameContext:          dir,
			builder.DefaultLocalNameDockerfile + "2": dir2,
		},
		Exports: []client.ExportEntry{
			{
				Type:      client.ExporterLocal,
				OutputDir: destDir,
			},
		},
	}, "", b, nil)
	require.NoError(t, err)

	for _, platform := range platforms {
		require.Contains(t, res.ExporterResponse, fmt.Sprintf("%s/%s", exptypes.ExporterBuildInfo, platform))
		dtbi, err := base64.StdEncoding.DecodeString(res.ExporterResponse[fmt.Sprintf("%s/%s", exptypes.ExporterBuildInfo, platform)])
		require.NoError(t, err)

		var bi binfotypes.BuildInfo
		err = json.Unmarshal(dtbi, &bi)
		require.NoError(t, err)

		require.Contains(t, bi.Attrs, "context:base")
		require.Equal(t, "input:base", *bi.Attrs["context:base"])

		require.Equal(t, 1, len(bi.Sources))
		assert.Equal(t, binfotypes.SourceTypeDockerImage, bi.Sources[0].Type)
		assert.Equal(t, "docker.io/library/busybox:latest", bi.Sources[0].Ref)
		assert.NotEmpty(t, bi.Sources[0].Pin)

		require.Contains(t, bi.Deps, "base")
		depsrc := bi.Deps["base"].Sources
		require.Equal(t, 1, len(depsrc))
		assert.Equal(t, binfotypes.SourceTypeDockerImage, depsrc[0].Type)
		assert.Equal(t, "docker.io/library/alpine:latest", depsrc[0].Ref)
		assert.NotEmpty(t, depsrc[0].Pin)
	}
}

func testBuildInfoDepsMainNoSource(t *testing.T, sb integration.Sandbox) {
	ctx := sb.Context()
	f := getFrontend(t, sb)
	f.RequiresBuildctl(t)

	c, err := client.New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	dockerfile := []byte(`
FROM alpine
ENV FOO=bar
ADD https://raw.githubusercontent.com/moby/moby/master/README.md /
RUN echo first > /out
`)

	dir, err := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)
	require.NoError(t, err)

	dockerfile2 := []byte(`
FROM base AS build
RUN echo "foo is $FOO" > /foo
`)

	dir2, err := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile2, 0600),
	)
	require.NoError(t, err)

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

		dtic, ok := res.Metadata[exptypes.ExporterImageConfigKey]
		if !ok {
			return nil, errors.Errorf("no containerimage.config in metadata")
		}

		dtbi, ok := res.Metadata[exptypes.ExporterBuildInfo]
		if !ok {
			return nil, errors.Errorf("no containerimage.buildinfo in metadata")
		}

		dt, err := json.Marshal(map[string][]byte{
			exptypes.ExporterImageConfigKey: dtic,
			exptypes.ExporterBuildInfo:      dtbi,
		})
		if err != nil {
			return nil, err
		}

		res, err = f.SolveGateway(ctx, c, gateway.SolveRequest{
			FrontendOpt: map[string]string{
				"dockerfilekey":       builder.DefaultLocalNameDockerfile + "2",
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

	destDir := t.TempDir()

	res, err := c.Build(ctx, client.SolveOpt{
		LocalDirs: map[string]string{
			builder.DefaultLocalNameDockerfile:       dir,
			builder.DefaultLocalNameContext:          dir,
			builder.DefaultLocalNameDockerfile + "2": dir2,
		},
		Exports: []client.ExportEntry{
			{
				Type:      client.ExporterLocal,
				OutputDir: destDir,
			},
		},
	}, "", b, nil)
	require.NoError(t, err)

	require.Contains(t, res.ExporterResponse, exptypes.ExporterBuildInfo)
	dtbi, err := base64.StdEncoding.DecodeString(res.ExporterResponse[exptypes.ExporterBuildInfo])
	require.NoError(t, err)

	var bi binfotypes.BuildInfo
	err = json.Unmarshal(dtbi, &bi)
	require.NoError(t, err)

	require.Contains(t, bi.Attrs, "context:base")
	require.Equal(t, "input:base", *bi.Attrs["context:base"])

	require.Equal(t, 1, len(bi.Sources))

	assert.Equal(t, binfotypes.SourceTypeHTTP, bi.Sources[0].Type)
	assert.Equal(t, "https://raw.githubusercontent.com/moby/moby/master/README.md", bi.Sources[0].Ref)
	assert.Equal(t, "sha256:419455202b0ef97e480d7f8199b26a721a417818bc0e2d106975f74323f25e6c", bi.Sources[0].Pin)

	require.Contains(t, bi.Deps, "base")
	depsrc := bi.Deps["base"].Sources
	require.Equal(t, 1, len(depsrc))
	assert.Equal(t, binfotypes.SourceTypeDockerImage, depsrc[0].Type)
	assert.Equal(t, "docker.io/library/alpine:latest", depsrc[0].Ref)
	assert.NotEmpty(t, depsrc[0].Pin)
}
