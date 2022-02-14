package dockerfile

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/ioutil"
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
	binfotypes "github.com/moby/buildkit/util/buildinfo/types"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var buildinfoTests = integration.TestFuncs(
	testBuildSources,
	testBuildAttrs,
	testBuildInfoMultiPlatform,
)

func init() {
	allTests = append(allTests, buildinfoTests...)
}

// moby/buildkit#2311
func testBuildSources(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	gitDir, err := ioutil.TempDir("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(gitDir)

	dockerfile := `
FROM alpine:latest@sha256:21a3deaa0d32a8057914f36584b5288d2e5ecc984380bc0118285c70fa8c9300 AS alpine
FROM busybox:latest
ADD https://raw.githubusercontent.com/moby/moby/master/README.md /
COPY --from=alpine /bin/busybox /alpine-busybox
`

	err = ioutil.WriteFile(filepath.Join(gitDir, "Dockerfile"), []byte(dockerfile), 0600)
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

	destDir, err := ioutil.TempDir("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(destDir)

	out := filepath.Join(destDir, "out.tar")
	outW, err := os.Create(out)
	require.NoError(t, err)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	res, err := f.Solve(sb.Context(), c, client.SolveOpt{
		Exports: []client.ExportEntry{
			{
				Type:   client.ExporterOCI,
				Output: fixedWriteCloser(outW),
			},
		},
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

// moby/buildkit#2476
func testBuildAttrs(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)
	f.RequiresBuildctl(t)

	dockerfile := `
FROM busybox:latest
ARG foo
RUN echo $foo
`

	dir, err := tmpdir(
		fstest.CreateFile("Dockerfile", []byte(dockerfile), 0600),
	)
	require.NoError(t, err)
	defer os.RemoveAll(dir)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	destDir, err := ioutil.TempDir("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(destDir)

	out := filepath.Join(destDir, "out.tar")
	outW, err := os.Create(out)
	require.NoError(t, err)

	res, err := f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"build-arg:foo": "bar",
		},
		Exports: []client.ExportEntry{
			{
				Type:   client.ExporterOCI,
				Output: fixedWriteCloser(outW),
			},
		},
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
	require.Equal(t, "bar", bi.Attrs["build-arg:foo"])
}

// moby/buildkit#2476
func testBuildInfoMultiPlatform(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)
	f.RequiresBuildctl(t)

	dockerfile := `
FROM busybox:latest
ARG foo
RUN echo $foo
ADD https://raw.githubusercontent.com/moby/moby/master/README.md /
`

	dir, err := tmpdir(
		fstest.CreateFile("Dockerfile", []byte(dockerfile), 0600),
	)
	require.NoError(t, err)
	defer os.RemoveAll(dir)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	destDir, err := ioutil.TempDir("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(destDir)

	out := filepath.Join(destDir, "out.tar")
	outW, err := os.Create(out)
	require.NoError(t, err)

	platforms := []string{"linux/amd64", "linux/arm64"}

	res, err := f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"build-arg:foo": "bar",
			"platform":      strings.Join(platforms, ","),
		},
		Exports: []client.ExportEntry{
			{
				Type:   client.ExporterOCI,
				Output: fixedWriteCloser(outW),
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
		require.Equal(t, "bar", bi.Attrs["build-arg:foo"])

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
