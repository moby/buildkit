package dockerfile

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/containerd/continuity/fs/fstest"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/frontend/dockerui"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/stretchr/testify/require"
	"github.com/tonistiigi/fsutil"
)

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

func testDefaultEnvWithArgs(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(integration.UnixOrWindows(
		`
ARG image=idlebox
FROM busy${image#idle} AS build
ARG my_arg
ENV my_arg "my_arg=${my_arg:-def_val}"
ENV my_trimmed_arg "${my_arg%%e*}"
COPY myscript.sh myscript.sh
RUN ./myscript.sh $my_arg $my_trimmed_arg
FROM scratch
COPY --from=build /out /out
`,
		`
FROM nanoserver AS build
USER ContainerAdministrator
ARG my_arg
ENV my_arg "my_arg=${my_arg:-def_val}"
ENV my_trimmed_arg "${my_arg%%e*}"
COPY myscript.cmd myscript.cmd
RUN myscript.cmd
FROM nanoserver
COPY --from=build /out /out
`,
	))

	script := []byte(integration.UnixOrWindows(
		`
#!/usr/bin/env sh
echo -n $my_arg $* > /out
`,
		"@echo off\r\necho %my_arg% %my_arg% %my_trimmed_arg% > out\r\n",
	))

	scriptName := integration.UnixOrWindows("myscript.sh", "myscript.cmd")
	scriptMode := integration.UnixOrWindows(os.FileMode(0700), os.FileMode(0600))

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile(scriptName, script, scriptMode),
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

			actual := integration.UnixOrWindows(string(dt), strings.TrimSpace(string(dt)))
			require.Equal(t, x.expected, actual)
		})
	}
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
