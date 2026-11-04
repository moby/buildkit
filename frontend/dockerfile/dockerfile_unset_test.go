//go:build dfunset

package dockerfile

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/containerd/continuity/fs/fstest"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/frontend/dockerui"
	"github.com/moby/buildkit/util/contentutil"
	"github.com/moby/buildkit/util/testutil"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/moby/buildkit/util/testutil/workers"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	"github.com/tonistiigi/fsutil"
)

func init() {
	allTests = append(allTests, integration.TestFuncs(
		testUnsetImageConfig,
		testUnsetRunEnv,
		testUnsetInvalid,
	)...)
}

func testUnsetImageConfig(t *testing.T, sb integration.Sandbox) {
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

	dockerfile := []byte(`
FROM busybox
ENV FOO=foo BAR=bar BAZ=baz
LABEL a=1 b=2 c=3
VOLUME ["/data/", "/cache", "/keep"]
`)
	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	baseTarget := registry + "/buildkit/testunset:base"
	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		Exports: []client.ExportEntry{
			{
				Type: client.ExporterImage,
				Attrs: map[string]string{
					"name": baseTarget,
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

	dockerfile = []byte(`
FROM ` + baseTarget + ` AS base
ENV --unset FOO BAZ
LABEL --unset a
ARG name=b
LABEL --unset ${name}
VOLUME --unset /data /cache/ /notexist
ARG BOTH=arg
ENV BOTH=env
ENV --unset BOTH
RUN true

FROM base
ENV --unset=false NEW=new
`)
	dir = integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	target := registry + "/buildkit/testunset:child"
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

	desc, provider, err := contentutil.ProviderFromRef(sb.Context(), target)
	require.NoError(t, err)

	img, err := testutil.ReadImage(sb.Context(), provider, desc)
	require.NoError(t, err)

	var envKeys []string
	for _, env := range img.Img.Config.Env {
		k, _, _ := strings.Cut(env, "=")
		envKeys = append(envKeys, k)
	}
	require.Contains(t, envKeys, "PATH")
	require.Contains(t, img.Img.Config.Env, "BAR=bar")
	require.Contains(t, img.Img.Config.Env, "NEW=new")
	require.NotContains(t, envKeys, "FOO")
	require.NotContains(t, envKeys, "BAZ")

	require.Equal(t, map[string]string{"c": "3"}, img.Img.Config.Labels)

	require.Equal(t, []string{"/keep"}, slices.Collect(maps.Keys(img.Img.Config.Volumes)))

	var history []string
	for _, h := range img.Img.History {
		history = append(history, h.CreatedBy)
	}
	require.Contains(t, history, "ENV --unset FOO BAZ")
	require.Contains(t, history, "LABEL --unset b")
	require.Contains(t, history, "VOLUME --unset [/data /cache/ /notexist]")

	var runHistory string
	for _, h := range history {
		if strings.Contains(h, "/bin/sh -c true") {
			runHistory = h
		}
	}
	require.NotEmpty(t, runHistory)
	require.NotContains(t, runHistory, "BOTH")
}

func testUnsetRunEnv(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM busybox AS base
ARG FROMCLI
ENV ENVVAR=env KEEP=keep
RUN env | sort > /env0
ENV --unset ENVVAR FROMCLI
ENV EXPANDED=${ENVVAR:-none}-${FROMCLI:-none}
RUN env | sort > /env1

ARG BOTH=arg
ENV BOTH=env
ENV --unset BOTH
RUN env | sort > /env2

ONBUILD ENV --unset KEEP

FROM base AS child
RUN env | sort > /env3
ARG FROMCLI
RUN env | sort > /env4

FROM scratch
COPY --from=child /env0 /env1 /env2 /env3 /env4 /
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
			"build-arg:FROMCLI": "cli",
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

	dt, err := os.ReadFile(filepath.Join(destDir, "env0"))
	require.NoError(t, err)
	env := string(dt)
	require.Contains(t, env, "ENVVAR=env\n")
	require.Contains(t, env, "FROMCLI=cli\n")

	dt, err = os.ReadFile(filepath.Join(destDir, "env1"))
	require.NoError(t, err)
	env = string(dt)
	require.Contains(t, env, "KEEP=keep\n")
	require.Contains(t, env, "EXPANDED=none-none\n")
	require.NotContains(t, env, "ENVVAR=")
	require.NotContains(t, env, "FROMCLI=")

	dt, err = os.ReadFile(filepath.Join(destDir, "env2"))
	require.NoError(t, err)
	env = string(dt)
	require.NotContains(t, env, "BOTH=")

	dt, err = os.ReadFile(filepath.Join(destDir, "env3"))
	require.NoError(t, err)
	env = string(dt)
	require.NotContains(t, env, "KEEP=")
	require.NotContains(t, env, "FROMCLI=")
	require.Contains(t, env, "EXPANDED=none-none\n")

	dt, err = os.ReadFile(filepath.Join(destDir, "env4"))
	require.NoError(t, err)
	env = string(dt)
	require.Contains(t, env, "FROMCLI=cli\n")
}

func testUnsetInvalid(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	for _, tc := range []struct {
		dockerfile string
		err        string
	}{
		{
			dockerfile: "FROM scratch\nENV --unset FOO=bar\n",
			err:        "ENV --unset does not accept values: FOO=bar",
		},
		{
			dockerfile: "FROM scratch\nLABEL --unset foo=bar\n",
			err:        "LABEL --unset does not accept values: foo=bar",
		},
		{
			dockerfile: "FROM scratch\nENV --unset\n",
			err:        "ENV --unset requires at least one argument",
		},
		{
			dockerfile: "FROM scratch\nARG --unset FOO\n",
			err:        "unknown flag: --unset",
		},
		{
			dockerfile: "FROM scratch\nENV --unset=yes FOO\n",
			err:        "expecting boolean value for flag --unset, not: yes",
		},
		{
			dockerfile: "FROM scratch\nARG EMPTY\nVOLUME --unset ${EMPTY}\n",
			err:        "VOLUME specified can not be an empty string",
		},
		{
			dockerfile: "FROM scratch\nARG EMPTY\nENV --unset ${EMPTY}\n",
			err:        "ENV --unset names can not be blank",
		},
		{
			dockerfile: "FROM scratch\nARG EMPTY\nLABEL --unset ${EMPTY}\n",
			err:        "LABEL --unset names can not be blank",
		},
	} {
		dir := integration.Tmpdir(
			t,
			fstest.CreateFile("Dockerfile", []byte(tc.dockerfile), 0600),
		)

		_, err = f.Solve(sb.Context(), c, client.SolveOpt{
			LocalMounts: map[string]fsutil.FS{
				dockerui.DefaultLocalNameDockerfile: dir,
				dockerui.DefaultLocalNameContext:    dir,
			},
		}, nil)
		require.ErrorContains(t, err, tc.err, tc.dockerfile)
	}
}
