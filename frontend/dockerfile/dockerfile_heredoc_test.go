package dockerfile

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/containerd/continuity/fs/fstest"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/frontend/dockerui"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/moby/buildkit/util/testutil/workers"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	"github.com/tonistiigi/fsutil"
)

var hdTests = integration.TestFuncs(
	testCopyHeredoc,
	testCopyHeredocSpecialSymbols,
	testRunBasicHeredoc,
	testRunFakeHeredoc,
	testRunShebangHeredoc,
	testRunComplexHeredoc,
	testHeredocIndent,
	testHeredocVarSubstitution,
	testOnBuildHeredoc,
)

func init() {
	heredocTests = append(heredocTests, hdTests...)
}

// testCopyHeredoc verifies Dockerfile COPY with heredoc syntax for inline file creation.
// It tests single file, multiple files into a directory, --chmod, --chown permissions,
// and stat verification. On Windows, only core heredoc COPY is tested since --chmod,
// --chown, and stat are not supported.
func testCopyHeredoc(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	// Windows: --chmod, --chown, adduser, and stat are not supported on Windows containers.
	// The Windows variant tests only the core heredoc COPY functionality (inline file creation).
	dockerfileStr := integration.UnixOrWindows(
		`
FROM busybox AS build

RUN adduser -D user
WORKDIR /dest

COPY <<EOF single
single file
EOF

COPY <<EOF <<EOF2 double/
first file
EOF
second file
EOF2

RUN mkdir -p /permfiles
COPY --chmod=777 <<EOF /permfiles/all
dummy content
EOF
COPY --chmod=0644 <<EOF /permfiles/rw
dummy content
EOF
COPY --chown=user:user <<EOF /permfiles/owned
dummy content
EOF
RUN stat -c "%04a" /permfiles/all >> perms && \
	stat -c "%04a" /permfiles/rw >> perms && \
	stat -c "%U:%G" /permfiles/owned >> perms

FROM scratch
COPY --from=build /dest /
`,
		`
FROM nanoserver:latest AS build

WORKDIR /dest

COPY <<EOF single
single file
EOF

COPY <<EOF <<EOF2 double/
first file
EOF
second file
EOF2

FROM scratch
COPY --from=build /dest /
`)
	dockerfile := []byte(dockerfileStr)

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

	contents := integration.UnixOrWindows(
		map[string]string{
			"single":      "single file\n",
			"double/EOF":  "first file\n",
			"double/EOF2": "second file\n",
			"perms":       "0777\n0644\nuser:user\n",
		},
		map[string]string{
			"single":      "single file\n",
			"double/EOF":  "first file\n",
			"double/EOF2": "second file\n",
		},
	)

	for name, content := range contents {
		dt, err := os.ReadFile(filepath.Join(destDir, name))
		require.NoError(t, err)
		require.Equal(t, content, string(dt))
	}
}

// testCopyHeredocSpecialSymbols tests that COPY heredoc preserves special characters
// correctly. It creates files containing quotes, backslashes, and dollar signs, then
// verifies the difference between processed heredocs (<<EOF) where backslash sequences
// are interpreted, and raw heredocs (<<"EOF") where content is kept literal.
func testCopyHeredocSpecialSymbols(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM scratch

COPY <<EOF quotefile
"quotes in file"
EOF

COPY <<EOF slashfile1
\
EOF
COPY <<EOF slashfile2
\\
EOF
COPY <<EOF slashfile3
\$
EOF

COPY <<"EOF" rawslashfile1
\
EOF
COPY <<"EOF" rawslashfile2
\\
EOF
COPY <<"EOF" rawslashfile3
\$
EOF
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

	dt, err := os.ReadFile(filepath.Join(destDir, "quotefile"))
	require.NoError(t, err)
	require.Equal(t, "\"quotes in file\"\n", string(dt))

	dt, err = os.ReadFile(filepath.Join(destDir, "slashfile1"))
	require.NoError(t, err)
	require.Equal(t, "\n", string(dt))

	dt, err = os.ReadFile(filepath.Join(destDir, "slashfile2"))
	require.NoError(t, err)
	require.Equal(t, "\\\n", string(dt))

	dt, err = os.ReadFile(filepath.Join(destDir, "slashfile3"))
	require.NoError(t, err)
	require.Equal(t, "$\n", string(dt))

	dt, err = os.ReadFile(filepath.Join(destDir, "rawslashfile1"))
	require.NoError(t, err)
	require.Equal(t, "\\\n", string(dt))

	dt, err = os.ReadFile(filepath.Join(destDir, "rawslashfile2"))
	require.NoError(t, err)
	require.Equal(t, "\\\\\n", string(dt))

	dt, err = os.ReadFile(filepath.Join(destDir, "rawslashfile3"))
	require.NoError(t, err)
	require.Equal(t, "\\$\n", string(dt))
}

// testRunBasicHeredoc checks that a RUN instruction using heredoc syntax
// runs the commands in the heredoc body and writes the expected output to
// a file. Linux runs two separate commands inside the heredoc; Windows runs
// a single cmd command because BuildKit's Windows executor cannot pass
// multi-line bodies to a shell.
func testRunBasicHeredoc(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfileStr := integration.UnixOrWindows(
		`
FROM busybox AS build

RUN <<EOF
echo "i am" >> /dest
whoami >> /dest
EOF

FROM scratch
COPY --from=build /dest /dest
`,
		`
FROM nanoserver:latest AS build
USER ContainerAdministrator

RUN <<EOF
(echo i am&echo done)> C:\dest
EOF

FROM nanoserver:latest
COPY --from=build C:/dest /dest
`)
	dockerfile := []byte(dockerfileStr)

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

	dt, err := os.ReadFile(filepath.Join(destDir, "dest"))

	require.NoError(t, err)
	// Linux: busybox echo + whoami (running as root) -> "i am\nroot\n".
	// Windows: cmd `echo` for `(echo i am&echo done)> C:\dest` writes
	// "i am\r\ndone\r\n" (CRLF line endings, no trailing space).
	expectedContent := integration.UnixOrWindows("i am\nroot\n", "i am\r\ndone\r\n")
	require.Equal(t, expectedContent, string(dt))
}

// testRunFakeHeredoc verifies that the SHELL directive overrides the default
// interpreter used to run a RUN heredoc body, by using a non-default shell whose
// output would be impossible under the default one.
func testRunFakeHeredoc(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfileStr := integration.UnixOrWindows(
		`
FROM busybox AS build

SHELL ["/bin/awk"]
RUN <<EOF
BEGIN {
	print "foo" > "/dest"
}
EOF

FROM scratch
COPY --from=build /dest /dest
`,
		`
FROM nanoserver:latest AS build
USER ContainerAdministrator

SHELL ["cmd", "/U", "/C"]
RUN <<EOF
echo foo> C:\dest
EOF

FROM nanoserver:latest
COPY --from=build C:/dest /dest
`)
	dockerfile := []byte(dockerfileStr)

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

	dt, err := os.ReadFile(filepath.Join(destDir, "dest"))
	require.NoError(t, err)
	// Negative control on Windows: under the default SHELL (`cmd /S /C`), the
	// same `echo foo> C:\dest` would write the ASCII bytes "foo \r\n" (cmd
	// preserves the space before `>`). Asserting we did NOT get that proves the
	// SHELL override actually applied /U rather than the default flags.
	if runtime.GOOS == "windows" {
		require.NotEqual(t, "foo \r\n", string(dt))
	}

	// Linux: awk writes "foo\n". Windows: cmd /U /C forces internal commands to
	// write UTF-16LE on redirection. `echo foo> C:\dest` therefore produces the
	// UTF-16LE bytes for "foo\r\n". Under the default cmd /S /C this file would
	// be ASCII, so a match here proves the SHELL override actually changed cmd
	// flags.
	expectedContent := integration.UnixOrWindows("foo\n", "f\x00o\x00o\x00\r\x00\n\x00")
	require.Equal(t, expectedContent, string(dt))
}

// testRunShebangHeredoc tests that RUN heredocs with a shebang line (#!/bin/awk -f)
// are executed by the interpreter specified in the shebang, not the default shell.
// It also tests that <<-EOF (dash prefix) correctly strips leading tabs from the
// heredoc content before passing it to the interpreter.
func testRunShebangHeredoc(t *testing.T, sb integration.Sandbox) {
	// Skipped on Windows:
	// Uses shebangs (#!/bin/awk -f) which are a Linux-specific feature. Windows
	// does not support shebang lines in scripts.
	// No workaround: Shebangs are fundamentally a Unix concept with no Windows equivalent.
	integration.SkipOnPlatform(t, "windows", "Shebangs not supported on Windows")
	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM busybox AS build

WORKDIR /dest

RUN <<EOF
#!/bin/awk -f
BEGIN {
	print "hello" >> "./out1"
	print "world" >> "./out1"
}
EOF

RUN <<-EOF
	#!/bin/awk -f
	BEGIN {
		print "hello" >> "./out2"
		print "world" >> "./out2"
	}
EOF

FROM scratch
COPY --from=build /dest /
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

	contents := map[string]string{
		"out1": "hello\nworld\n",
		"out2": "hello\nworld\n",
	}

	for name, content := range contents {
		dt, err := os.ReadFile(filepath.Join(destDir, name))
		require.NoError(t, err)
		require.Equal(t, content, string(dt))
	}
}

// testRunComplexHeredoc tests advanced heredoc scenarios: piping heredoc content
// through commands like tr for case conversion, using shell line continuations with
// multiple heredocs in a single RUN, and redirecting multiple heredocs to different
// file descriptors (3<<IN1 4<<IN2) for awk to process simultaneously.
func testRunComplexHeredoc(t *testing.T, sb integration.Sandbox) {
	// Skipped on Windows:
	// Uses Linux-specific shell features: piping (|), tr, awk, /proc/self/fd/,
	// and multiple file descriptor redirections (3<<IN1 4<<IN2).
	// No workaround: These features have no equivalent in Windows cmd.
	integration.SkipOnPlatform(t, "windows", "Linux shell piping, tr, awk, and fd redirections not available on Windows")
	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM busybox AS build

WORKDIR /dest

RUN cat <<EOF1 | tr '[:upper:]' '[:lower:]' > ./out1; \
	cat <<EOF2 | tr '[:lower:]' '[:upper:]' > ./out2
hello WORLD
EOF1
HELLO world
EOF2

RUN <<EOF 3<<IN1 4<<IN2 awk -f -
BEGIN {
	while ((getline line < "/proc/self/fd/3") > 0)
		print tolower(line) > "./fd3"
	while ((getline line < "/proc/self/fd/4") > 0)
		print toupper(line) > "./fd4"
}
EOF
hello WORLD
IN1
HELLO world
IN2

FROM scratch
COPY --from=build /dest /
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

	contents := map[string]string{
		"out1": "hello world\n",
		"out2": "HELLO WORLD\n",
		"fd3":  "hello world\n",
		"fd4":  "HELLO WORLD\n",
	}

	for name, content := range contents {
		dt, err := os.ReadFile(filepath.Join(destDir, name))
		require.NoError(t, err)
		require.Equal(t, content, string(dt))
	}
}

// testHeredocIndent checks that <<EOF keeps leading tabs and <<-EOF removes
// them, for both COPY and RUN heredocs. Linux runs the full set of cases
// (plain shell, shebang scripts, and shell redirects). Windows runs only the
// COPY cases, since the RUN cases need shebangs and shell redirects that
// Windows does not support.
func testHeredocIndent(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfileStr := integration.UnixOrWindows(
		`
FROM busybox AS build

COPY <<EOF /dest/foo-copy
	foo
EOF

COPY <<-EOF /dest/bar-copy
	bar
EOF

RUN <<EOF
echo "
	foo" > /dest/foo-run
EOF

RUN <<-EOF
echo "
	bar" > /dest/bar-run
EOF

RUN <<EOF
#!/bin/sh
echo "
	foo" > /dest/foo2-run
EOF

RUN <<-EOF
#!/bin/sh
echo "
	bar" > /dest/bar2-run
EOF

RUN <<EOF sh > /dest/foo3-run
echo "
	foo"
EOF

RUN <<-EOF sh > /dest/bar3-run
echo "
	bar"
EOF

FROM scratch
COPY --from=build /dest /
`,

		`
FROM nanoserver:latest AS build

COPY <<EOF /dest/foo-copy
	foo
EOF

COPY <<-EOF /dest/bar-copy
	bar
EOF

FROM scratch
COPY --from=build /dest /
`)
	dockerfile := []byte(dockerfileStr)

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

	contents := integration.UnixOrWindows(
		map[string]string{
			"foo-copy": "\tfoo\n",
			"foo-run":  "\n\tfoo\n",
			"foo2-run": "\n\tfoo\n",
			"foo3-run": "\n\tfoo\n",
			"bar-copy": "bar\n",
			"bar-run":  "\nbar\n",
			"bar2-run": "\nbar\n",
			"bar3-run": "\nbar\n",
		},
		map[string]string{
			"foo-copy": "\tfoo\n",
			"bar-copy": "bar\n",
		},
	)

	for name, content := range contents {
		dt, err := os.ReadFile(filepath.Join(destDir, name))
		require.NoError(t, err)
		require.Equal(t, content, string(dt))
	}
}

// testHeredocVarSubstitution checks that Dockerfile ARG values are substituted
// inside heredocs: unquoted <<EOF expands ${name}, while <<'EOF' and <<"EOF"
// keep it literal. Linux also covers RUN heredocs with shell variable
// shadowing; Windows runs only the COPY cases plus a single-line RUN heredoc
// using cmd's %name% syntax, since the shadowing cases need multi-line bodies
// that BuildKit's Windows executor does not support.
func testHeredocVarSubstitution(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfileStr := integration.UnixOrWindows(
		`
FROM busybox as build

ARG name=world

COPY <<EOF /dest/c1
Hello ${name}!
EOF
COPY <<'EOF' /dest/c2
Hello ${name}!
EOF
COPY <<"EOF" /dest/c3
Hello ${name}!
EOF

COPY <<EOF /dest/q1
Hello '${name}'!
EOF
COPY <<EOF /dest/q2
Hello "${name}"!
EOF
COPY <<'EOF' /dest/qsingle1
Hello '${name}'!
EOF
COPY <<'EOF' /dest/qsingle2
Hello "${name}"!
EOF
COPY <<"EOF" /dest/qdouble1
Hello '${name}'!
EOF
COPY <<"EOF" /dest/qdouble2
Hello "${name}"!
EOF

RUN <<EOF
greeting="Hello"
echo "${greeting} ${name}!" > /dest/r1
EOF
RUN <<EOF
name="new world"
echo "Hello ${name}!" > /dest/r2
EOF

FROM scratch
COPY --from=build /dest /
`,
		`
FROM nanoserver:latest as build

ARG name=world

COPY <<EOF /dest/c1
Hello ${name}!
EOF
COPY <<'EOF' /dest/c2
Hello ${name}!
EOF
COPY <<"EOF" /dest/c3
Hello ${name}!
EOF

COPY <<EOF /dest/q1
Hello '${name}'!
EOF
COPY <<EOF /dest/q2
Hello "${name}"!
EOF
COPY <<'EOF' /dest/qsingle1
Hello '${name}'!
EOF
COPY <<'EOF' /dest/qsingle2
Hello "${name}"!
EOF
COPY <<"EOF" /dest/qdouble1
Hello '${name}'!
EOF
COPY <<"EOF" /dest/qdouble2
Hello "${name}"!
EOF

USER ContainerAdministrator
RUN <<EOF
(echo Hello %name%!)> C:\dest\r1
EOF

FROM scratch
COPY --from=build /dest /
`)
	dockerfile := []byte(dockerfileStr)

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

	contents := integration.UnixOrWindows(
		map[string]string{
			"c1":       "Hello world!\n",
			"c2":       "Hello ${name}!\n",
			"c3":       "Hello ${name}!\n",
			"q1":       "Hello 'world'!\n",
			"q2":       "Hello \"world\"!\n",
			"qsingle1": "Hello '${name}'!\n",
			"qsingle2": "Hello \"${name}\"!\n",
			"qdouble1": "Hello '${name}'!\n",
			"qdouble2": "Hello \"${name}\"!\n",
			"r1":       "Hello world!\n",
			"r2":       "Hello new world!\n",
		},
		map[string]string{
			"c1":       "Hello world!\n",
			"c2":       "Hello ${name}!\n",
			"c3":       "Hello ${name}!\n",
			"q1":       "Hello 'world'!\n",
			"q2":       "Hello \"world\"!\n",
			"qsingle1": "Hello '${name}'!\n",
			"qsingle2": "Hello \"${name}\"!\n",
			"qdouble1": "Hello '${name}'!\n",
			"qdouble2": "Hello \"${name}\"!\n",
			"r1":       "Hello world!\r\n",
		},
	)

	for name, content := range contents {
		dt, err := os.ReadFile(filepath.Join(destDir, name))
		require.NoError(t, err)
		require.Equal(t, content, string(dt))
	}
}

// testOnBuildHeredoc tests that ONBUILD triggers can use RUN with heredoc syntax.
// It builds and pushes a base image containing an ONBUILD RUN <<EOF instruction,
// then builds a child image FROM that base and verifies the heredoc command was
// executed during the child build. Works on both Linux (busybox) and Windows
// (nanoserver).
func testOnBuildHeredoc(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureDirectPush)
	f := getFrontend(t, sb)

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	baseImage := integration.UnixOrWindows("busybox", "nanoserver:latest")
	echoCmd := integration.UnixOrWindows(`echo "hello world" >> /dest`, `echo hello world > /dest`)
	userDirective := integration.UnixOrWindows("", "USER ContainerAdministrator\n")

	dockerfile := fmt.Appendf(nil, `
FROM %s
%sONBUILD RUN <<EOF
%s
EOF
`, baseImage, userDirective, echoCmd)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	target := registry + "/buildkit/testonbuildheredoc:base"
	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		Exports: []client.ExportEntry{
			{
				Type: client.ExporterImage,
				Attrs: map[string]string{
					"push": "true",
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
	FROM %s AS base
	FROM scratch
	COPY --from=base /dest /dest
	`, target)

	dir = integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
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

	dt, err := os.ReadFile(filepath.Join(destDir, "dest"))
	require.NoError(t, err)
	// Windows cmd echo adds trailing space and uses CRLF line endings
	expectedContent := integration.UnixOrWindows("hello world\n", "hello world \r\n")
	require.Equal(t, expectedContent, string(dt))
}
