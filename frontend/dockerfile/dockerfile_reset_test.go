//go:build dfreset

package dockerfile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/continuity/fs/fstest"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/frontend/dockerui"
	"github.com/moby/buildkit/util/contentutil"
	"github.com/moby/buildkit/util/system"
	"github.com/moby/buildkit/util/testutil"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/moby/buildkit/util/testutil/workers"
	dockerspec "github.com/moby/docker-image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	"github.com/tonistiigi/fsutil"
)

func init() {
	allTests = append(allTests, integration.TestFuncs(
		testResetImageConfig,
		testResetStageBase,
		testResetOnbuildDeps,
		testResetInvalid,
	)...)
}

func testResetImageConfig(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	workers.CheckFeatureCompat(t, sb, workers.FeatureDirectPush)
	f := getFrontend(t, sb)

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	solve := func(dockerfile, target string) {
		dir := integration.Tmpdir(
			t,
			fstest.CreateFile("Dockerfile", []byte(dockerfile), 0600),
		)
		_, err := f.Solve(sb.Context(), c, client.SolveOpt{
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
	}

	baseTarget := registry + "/buildkit/testreset:base"
	solve(`
FROM busybox
RUN mkdir -m 0777 /work
ENV FOO=foo
LABEL a=1
VOLUME /data
EXPOSE 8080
HEALTHCHECK CMD true
SHELL ["/bin/sh", "-ec"]
STOPSIGNAL SIGINT
WORKDIR /work
USER 1000
ONBUILD RUN touch /tmp/onbuild
ENTRYPOINT ["/bin/echo"]
CMD ["hello"]
`, baseTarget)

	allTarget := registry + "/buildkit/testreset:all"
	solve(`
FROM --reset=all `+baseTarget+`
RUN [ ! -e /tmp/onbuild ] && [ "$(id -u)" = 0 ] && [ "$(pwd)" = / ] && [ -z "${FOO:-}" ]
`, allTarget)

	readConfig := func(target string) dockerspec.DockerOCIImageConfig {
		desc, provider, err := contentutil.ProviderFromRef(sb.Context(), target)
		require.NoError(t, err)
		img, err := testutil.ReadImage(sb.Context(), provider, desc)
		require.NoError(t, err)
		dt, err := content.ReadBlob(sb.Context(), provider, img.Manifest.Config)
		require.NoError(t, err)
		var cfg dockerspec.DockerOCIImage
		require.NoError(t, json.Unmarshal(dt, &cfg))
		return cfg.Config
	}

	cfg := readConfig(allTarget)
	require.Equal(t, []string{"PATH=" + system.DefaultPathEnvUnix}, cfg.Env)
	require.Empty(t, cfg.Labels)
	require.Empty(t, cfg.Volumes)
	require.Empty(t, cfg.ExposedPorts)
	require.Nil(t, cfg.Healthcheck)
	require.Empty(t, cfg.Shell)
	require.Empty(t, cfg.StopSignal)
	require.Empty(t, cfg.WorkingDir)
	require.Empty(t, cfg.User)
	require.Empty(t, cfg.OnBuild)
	require.Empty(t, cfg.Entrypoint)
	require.Empty(t, cfg.Cmd)

	partialTarget := registry + "/buildkit/testreset:partial"
	solve(`
FROM --reset=volume --reset=label,volume `+baseTarget+`
RUN [ -e /tmp/onbuild ] && [ "$(id -u)" = 1000 ] && [ "$(pwd)" = /work ] && [ "$FOO" = foo ]
`, partialTarget)

	cfg = readConfig(partialTarget)
	require.Empty(t, cfg.Labels)
	require.Empty(t, cfg.Volumes)
	require.Contains(t, cfg.Env, "FOO=foo")
	require.Contains(t, cfg.ExposedPorts, "8080/tcp")
	require.NotNil(t, cfg.Healthcheck)
	require.Equal(t, []string{"/bin/sh", "-ec"}, cfg.Shell)
	require.Equal(t, "SIGINT", cfg.StopSignal)
	require.Equal(t, "/work", cfg.WorkingDir)
	require.Equal(t, "1000", cfg.User)
	require.Equal(t, []string{"/bin/echo"}, cfg.Entrypoint)
	require.Equal(t, []string{"hello"}, cfg.Cmd)

	// relative WORKDIR after a workdir reset doesn't depend on the base image
	solve(`# check=error=true
FROM --reset=workdir `+baseTarget+`
WORKDIR app
RUN [ "$(pwd)" = /app ]
`, registry+"/buildkit/testreset:workdir")
}

func testResetStageBase(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM busybox AS base
RUN mkdir -m 0777 /work
ENV FOO=foo
ARG BAR=bar
WORKDIR /work
USER 1000
ONBUILD RUN touch /tmp/onbuild

FROM --reset=env,workdir,user,onbuild base AS reset
RUN env | sort > /tmp/env && pwd > /tmp/pwd && id -u > /tmp/uid && ls /tmp > /tmp/ls

FROM base AS keep
RUN pwd > /tmp/pwd && id -u > /tmp/uid && ls /tmp > /tmp/ls

FROM scratch
COPY --from=reset /tmp/env /tmp/pwd /tmp/uid /tmp/ls /reset/
COPY --from=keep /tmp/pwd /tmp/uid /tmp/ls /keep/
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

	read := func(p string) string {
		dt, err := os.ReadFile(filepath.Join(destDir, p))
		require.NoError(t, err)
		return string(dt)
	}

	env := read("reset/env")
	require.NotContains(t, env, "FOO=")
	require.Contains(t, env, "BAR=bar\n")
	require.Contains(t, env, "PATH=")
	require.Equal(t, "/\n", read("reset/pwd"))
	require.Equal(t, "0\n", read("reset/uid"))
	require.NotContains(t, read("reset/ls"), "onbuild")

	require.Equal(t, "/work\n", read("keep/pwd"))
	require.Equal(t, "1000\n", read("keep/uid"))
	require.Contains(t, read("keep/ls"), "onbuild")
}

// testResetOnbuildDeps checks that skipped ONBUILD triggers don't add
// dependencies to the build.
func testResetOnbuildDeps(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM busybox AS base
ONBUILD COPY --from=doesnotexist /foo /foo

FROM --reset=onbuild base
RUN true
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

func testResetInvalid(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	for _, tc := range []struct {
		value string
		err   string
	}{
		{value: "volumes", err: `invalid FROM --reset value: "volumes" (did you mean volume?)`},
		{value: "ENV", err: `invalid FROM --reset value: "ENV" (did you mean env?)`},
		{value: "ALL", err: `invalid FROM --reset value: "ALL" (did you mean all?)`},
	} {
		dir := integration.Tmpdir(
			t,
			fstest.CreateFile("Dockerfile", []byte("FROM --reset="+tc.value+" scratch\n"), 0600),
		)

		_, err = f.Solve(sb.Context(), c, client.SolveOpt{
			LocalMounts: map[string]fsutil.FS{
				dockerui.DefaultLocalNameDockerfile: dir,
				dockerui.DefaultLocalNameContext:    dir,
			},
		}, nil)
		require.ErrorContains(t, err, tc.err)
	}
}
