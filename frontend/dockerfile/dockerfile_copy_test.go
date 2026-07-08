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
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/filesync"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tonistiigi/fsutil"
)

func testDockerfileCopyFromArgs(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM scratch
COPY --from=$FOO . .
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
	require.Error(t, err)
	require.ErrorContains(t, err, "variable expansion is not supported for --from, define a new stage with FROM using ARG from global scope as a workaround")
}

func testEmptyDestDir(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM busybox
ENV empty=""
COPY testfile $empty
RUN [ "$(cat testfile)" == "contents0" ]
`,
		`
FROM nanoserver
COPY testfile ''
RUN cmd /V:on /C "set /p tfcontent=<testfile \
	& if !tfcontent! NEQ contents0 (exit 1)"
`,
	))

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile("testfile", []byte("contents0"), 0600),
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

func testPreserveDestDirSlash(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM busybox
COPY testfile /sample/
RUN [ "$(cat /sample/testfile)" == "contents0" ]
`,
		`
FROM nanoserver
COPY testfile /sample/
RUN cmd /V:on /C "set /p tfcontent=<\sample\testfile \
	& if !tfcontent! NEQ contents0 (exit 1)"
`,
	))

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile("testfile", []byte("contents0"), 0600),
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

func testCopyLinkDotDestDir(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows", "COPY --link requires diffApply which is not supported on Windows. Set COPY --link=false for default COPY behavior")
	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM busybox
WORKDIR /var/www
COPY --link testfile .
RUN [ "$(cat testfile)" == "contents0" ]
`)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile("testfile", []byte("contents0"), 0600),
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

func testCopyLinkEmptyDestDir(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows", "COPY --link requires diffApply which is not supported on Windows. Set COPY --link=false for default COPY behavior")
	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM busybox
WORKDIR /var/www
ENV empty=""
COPY --link testfile $empty
RUN [ "$(cat testfile)" == "contents0" ]
`)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile("testfile", []byte("contents0"), 0600),
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

func testCopyChownExistingDir(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows", "Test validates Unix chown behavior with UIDs/GIDs using stat command, not applicable to Windows file permissions")
	f := getFrontend(t, sb)

	dockerfile := []byte(`
# Set up files and directories with known ownership
FROM busybox AS source
RUN touch /file && chown 100:200 /file \
 && mkdir -p /dir/subdir \
 && touch /dir/subdir/nestedfile \
 && chown 100:200 /dir \
 && chown 101:201 /dir/subdir \
 && chown 102:202 /dir/subdir/nestedfile

FROM busybox AS test_base
RUN mkdir -p /existingdir/existingsubdir \
 && touch /existingdir/existingfile \
 && chown 500:600 /existingdir \
 && chown 501:601 /existingdir/existingsubdir \
 && chown 501:601 /existingdir/existingfile


# Copy files from the source stage
FROM test_base AS copy_from
COPY --from=source /file .
# Copy to a non-existing target directory creates the target directory (as root), then copies the _contents_ of the source directory into it
COPY --from=source /dir /dir
# Copying to an existing target directory will copy the _contents_ of the source directory into it
COPY --from=source /dir/. /existingdir

RUN e="100:200"; p="/file"                         ; a=` + "`" + `stat -c "%u:%g" "$p"` + "`" + `; if [ "$a" != "$e" ]; then echo "incorrect ownership on $p. expected $e, got $a"; exit 1; fi \
 && e="0:0";     p="/dir"                          ; a=` + "`" + `stat -c "%u:%g" "$p"` + "`" + `; if [ "$a" != "$e" ]; then echo "incorrect ownership on $p. expected $e, got $a"; exit 1; fi \
 && e="101:201"; p="/dir/subdir"                   ; a=` + "`" + `stat -c "%u:%g" "$p"` + "`" + `; if [ "$a" != "$e" ]; then echo "incorrect ownership on $p. expected $e, got $a"; exit 1; fi \
 && e="102:202"; p="/dir/subdir/nestedfile"        ; a=` + "`" + `stat -c "%u:%g" "$p"` + "`" + `; if [ "$a" != "$e" ]; then echo "incorrect ownership on $p. expected $e, got $a"; exit 1; fi \
# Existing files and directories ownership should not be modified
 && e="500:600"; p="/existingdir"                  ; a=` + "`" + `stat -c "%u:%g" "$p"` + "`" + `; if [ "$a" != "$e" ]; then echo "incorrect ownership on $p. expected $e, got $a"; exit 1; fi \
 && e="501:601"; p="/existingdir/existingsubdir"   ; a=` + "`" + `stat -c "%u:%g" "$p"` + "`" + `; if [ "$a" != "$e" ]; then echo "incorrect ownership on $p. expected $e, got $a"; exit 1; fi \
 && e="501:601"; p="/existingdir/existingfile"     ; a=` + "`" + `stat -c "%u:%g" "$p"` + "`" + `; if [ "$a" != "$e" ]; then echo "incorrect ownership on $p. expected $e, got $a"; exit 1; fi \
# But new files and directories should maintain their ownership
 && e="101:201"; p="/existingdir/subdir"           ; a=` + "`" + `stat -c "%u:%g" "$p"` + "`" + `; if [ "$a" != "$e" ]; then echo "incorrect ownership on $p. expected $e, got $a"; exit 1; fi \
 && e="102:202"; p="/existingdir/subdir/nestedfile"; a=` + "`" + `stat -c "%u:%g" "$p"` + "`" + `; if [ "$a" != "$e" ]; then echo "incorrect ownership on $p. expected $e, got $a"; exit 1; fi


# Copy files from the source stage and chown them.
FROM test_base AS copy_from_chowned
COPY --from=source --chown=300:400 /file .
# Copy to a non-existing target directory creates the target directory (as root), then copies the _contents_ of the source directory into it
COPY --from=source --chown=300:400 /dir /dir
# Copying to an existing target directory copies the _contents_ of the source directory into it
COPY --from=source --chown=300:400 /dir/. /existingdir

RUN e="300:400"; p="/file"                         ; a=` + "`" + `stat -c "%u:%g" "$p"` + "`" + `; if [ "$a" != "$e" ]; then echo "incorrect ownership on $p. expected $e, got $a"; exit 1; fi \
 && e="300:400"; p="/dir"                          ; a=` + "`" + `stat -c "%u:%g" "$p"` + "`" + `; if [ "$a" != "$e" ]; then echo "incorrect ownership on $p. expected $e, got $a"; exit 1; fi \
 && e="300:400"; p="/dir/subdir"                   ; a=` + "`" + `stat -c "%u:%g" "$p"` + "`" + `; if [ "$a" != "$e" ]; then echo "incorrect ownership on $p. expected $e, got $a"; exit 1; fi \
 && e="300:400"; p="/dir/subdir/nestedfile"        ; a=` + "`" + `stat -c "%u:%g" "$p"` + "`" + `; if [ "$a" != "$e" ]; then echo "incorrect ownership on $p. expected $e, got $a"; exit 1; fi \
# Existing files and directories ownership should not be modified
 && e="500:600"; p="/existingdir"                  ; a=` + "`" + `stat -c "%u:%g" "$p"` + "`" + `; if [ "$a" != "$e" ]; then echo "incorrect ownership on $p. expected $e, got $a"; exit 1; fi \
 && e="501:601"; p="/existingdir/existingsubdir"   ; a=` + "`" + `stat -c "%u:%g" "$p"` + "`" + `; if [ "$a" != "$e" ]; then echo "incorrect ownership on $p. expected $e, got $a"; exit 1; fi \
 && e="501:601"; p="/existingdir/existingfile"     ; a=` + "`" + `stat -c "%u:%g" "$p"` + "`" + `; if [ "$a" != "$e" ]; then echo "incorrect ownership on $p. expected $e, got $a"; exit 1; fi \
# But new files and directories should be chowned
 && e="300:400"; p="/existingdir/subdir"           ; a=` + "`" + `stat -c "%u:%g" "$p"` + "`" + `; if [ "$a" != "$e" ]; then echo "incorrect ownership on $p. expected $e, got $a"; exit 1; fi \
 && e="300:400"; p="/existingdir/subdir/nestedfile"; a=` + "`" + `stat -c "%u:%g" "$p"` + "`" + `; if [ "$a" != "$e" ]; then echo "incorrect ownership on $p. expected $e, got $a"; exit 1; fi
`)

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

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"target": "copy_from",
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)
}

func testCopyWildcardCache(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM busybox AS base
COPY foo* files/
RUN cat /dev/urandom | head -c 100 | sha256sum > unique
COPY bar files/
FROM scratch
COPY --from=base unique /
`,
		`
FROM nanoserver AS base
USER ContainerAdministrator
WORKDIR /files
COPY foo* /files/
RUN echo test> /unique
COPY bar /files/
FROM nanoserver
COPY --from=base /unique /
`,
	))

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile("foo1", []byte("foo1-data"), 0600),
		fstest.CreateFile("foo2", []byte("foo2-data"), 0600),
		fstest.CreateFile("bar", []byte("bar-data"), 0600),
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

	dt, err := os.ReadFile(filepath.Join(destDir, "unique"))
	require.NoError(t, err)

	err = os.WriteFile(filepath.Join(dir.Name, "bar"), []byte("bar-data-mod"), 0600)
	require.NoError(t, err)

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

	dt2, err := os.ReadFile(filepath.Join(destDir, "unique"))
	require.NoError(t, err)
	require.Equal(t, string(dt), string(dt2))

	err = os.WriteFile(filepath.Join(dir.Name, "foo2"), []byte("foo2-data-mod"), 0600)
	require.NoError(t, err)

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

	dt2, err = os.ReadFile(filepath.Join(destDir, "unique"))
	expectedStr := string(dt)
	expectedStr = integration.UnixOrWindows(expectedStr, expectedStr+"\r\n")
	require.NoError(t, err)
	require.NotEqual(t, expectedStr, string(dt2))
}

func testEmptyWildcard(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM scratch
COPY foo nomatch* /
`,
		`
FROM nanoserver
COPY foo nomatch* /
`,
	))

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile("foo", []byte("contents0"), 0600),
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
	require.Equal(t, "contents0", string(dt))
}

func testCopyChownCreateDest(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows", "Uses adduser command and stat for Unix UID/GID validation, not applicable to Windows")
	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM busybox
ARG group
ENV owner user01
RUN adduser -D user
RUN adduser -D user01
COPY --chown=user:user . /dest
COPY --chown=${owner}:${group} . /dest01
RUN [ "$(stat -c "%U %G" /dest)" == "user user" ]
RUN [ "$(stat -c "%U %G" /dest01)" == "user01 user" ]
`)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"build-arg:group": "user",
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)
}

func testCopyThroughSymlinkContext(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM scratch
COPY link/foo .
`,
		`
FROM nanoserver AS build
COPY link/foo .
`,
	))

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.Symlink("sub", "link"),
		fstest.CreateDir("sub", 0700),
		fstest.CreateFile("sub/foo", []byte(`contents`), 0600),
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
	require.Equal(t, "contents", string(dt))
}

func testCopyThroughSymlinkMultiStage(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM busybox AS build
RUN mkdir -p /out/sub && ln -s /out/sub /sub && ln -s out/sub /sub2 && echo -n "data" > /sub/foo
FROM scratch
COPY --from=build /sub/foo .
COPY --from=build /sub2/foo bar
	`,
		`
FROM nanoserver AS build
RUN mkdir out\sub && mklink /D sub out\sub && mklink /D sub2 out\sub && echo data> sub\foo
FROM nanoserver
COPY --from=build /sub/foo .
COPY --from=build /sub2/foo bar
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

	dt, err := os.ReadFile(filepath.Join(destDir, "foo"))
	require.NoError(t, err)
	lineEnd := integration.UnixOrWindows("", "\r\n")
	require.Equal(t, fmt.Sprintf("data%s", lineEnd), string(dt))
}

func testCopySocket(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows", "Tests Unix socket file handling which is not supported on Windows in the same way")
	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM scratch
COPY . /
`)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateSocket("socket.sock", 0600),
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

	fi, err := os.Lstat(filepath.Join(destDir, "socket.sock"))
	require.NoError(t, err)
	// make sure socket is converted to regular file.
	require.Equal(t, true, fi.Mode().IsRegular())
}

// #1197
func testCopyFollowAllSymlinks(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM scratch
COPY foo /
COPY foo/sub bar
`,
		`
FROM nanoserver
COPY foo /
COPY foo/sub bar
`,
	))

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile("bar", []byte(`bar-contents`), 0600),
		fstest.CreateDir("foo", 0700),
		fstest.Symlink("../bar", "foo/sub"),
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

func testCopySymlinks(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM scratch
COPY foo /
COPY sub/l* alllinks/
`,
		`
FROM nanoserver
RUN mkdir alllinks
COPY foo /
COPY sub/l* alllinks/
`,
	))

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile("bar", []byte(`bar-contents`), 0600),
		fstest.Symlink("bar", "foo"),
		fstest.CreateDir("sub", 0700),
		fstest.CreateFile("sub/lfile", []byte(`lfile-contents`), 0600),
		fstest.Symlink("subfile", "sub/l0"),
		fstest.CreateFile("sub/subfile", []byte(`subfile-contents`), 0600),
		fstest.Symlink("second", "sub/l1"),
		fstest.Symlink("baz", "sub/second"),
		fstest.CreateFile("sub/baz", []byte(`baz-contents`), 0600),
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
	require.Equal(t, "bar-contents", string(dt))

	dt, err = os.ReadFile(filepath.Join(destDir, "alllinks/l0"))
	require.NoError(t, err)
	require.Equal(t, "subfile-contents", string(dt))

	dt, err = os.ReadFile(filepath.Join(destDir, "alllinks/lfile"))
	require.NoError(t, err)
	require.Equal(t, "lfile-contents", string(dt))

	dt, err = os.ReadFile(filepath.Join(destDir, "alllinks/l1"))
	require.NoError(t, err)
	require.Equal(t, "baz-contents", string(dt))
}

func testCopyChown(t *testing.T, sb integration.Sandbox) {
	// This test should work on Windows, but requires a proper image, and we will need
	// to check SIDs instead of UIDs.
	integration.SkipOnPlatform(t, "windows", "COPY --chown tests use Unix UIDs/GIDs and stat command; Windows would require SIDs and different validation approach")
	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM busybox AS base
ENV owner 1000
RUN mkdir -m 0777 /out
COPY --chown=daemon foo /
COPY --chown=1000:nobody bar /baz
ARG group
COPY --chown=${owner}:${group} foo /foobis
RUN stat -c "%U %G" /foo  > /out/fooowner
RUN stat -c "%u %G" /baz/sub  > /out/subowner
RUN stat -c "%u %G" /foobis  > /out/foobisowner
FROM scratch
COPY --from=base /out /
`)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile("foo", []byte(`foo-contents`), 0600),
		fstest.CreateDir("bar", 0700),
		fstest.CreateFile("bar/sub", nil, 0600),
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
		FrontendAttrs: map[string]string{
			"build-arg:group": "nobody",
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "fooowner"))
	require.NoError(t, err)
	require.Equal(t, "daemon daemon\n", string(dt))

	dt, err = os.ReadFile(filepath.Join(destDir, "subowner"))
	require.NoError(t, err)
	require.Equal(t, "1000 nobody\n", string(dt))

	dt, err = os.ReadFile(filepath.Join(destDir, "foobisowner"))
	require.NoError(t, err)
	require.Equal(t, "1000 nobody\n", string(dt))
}

func testCopyChmod(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM busybox AS base

RUN mkdir -m 0777 /out
COPY --chmod=0644 foo /
COPY --chmod=777 bar /baz
COPY --chmod=0 foo /foobis
COPY --chmod=777 foo /parent/foo
COPY --chmod=770 foo /parent/sub/foo

ARG mode
COPY --chmod=${mode} foo /footer

RUN stat -c "%04a" /foo  > /out/fooperm
RUN stat -c "%04a" /baz  > /out/barperm
RUN stat -c "%04a" /foobis  > /out/foobisperm
RUN stat -c "%04a" /parent  > /out/parentperm
RUN stat -c "%04a" /parent/sub  > /out/subparentperm
RUN stat -c "%04a" /footer  > /out/footerperm
FROM scratch
COPY --from=base /out /
`)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile("foo", []byte(`foo-contents`), 0600),
		fstest.CreateFile("bar", []byte(`bar-contents`), 0700),
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
		FrontendAttrs: map[string]string{
			"build-arg:mode": "755",
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)

	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "fooperm"))
	require.NoError(t, err)
	require.Equal(t, "0644\n", string(dt))

	dt, err = os.ReadFile(filepath.Join(destDir, "barperm"))
	require.NoError(t, err)
	require.Equal(t, "0777\n", string(dt))

	dt, err = os.ReadFile(filepath.Join(destDir, "foobisperm"))
	require.NoError(t, err)
	require.Equal(t, "0000\n", string(dt))

	dt, err = os.ReadFile(filepath.Join(destDir, "parentperm"))
	require.NoError(t, err)
	require.Equal(t, "0777\n", string(dt))

	dt, err = os.ReadFile(filepath.Join(destDir, "subparentperm"))
	require.NoError(t, err)
	require.Equal(t, "0770\n", string(dt))

	dt, err = os.ReadFile(filepath.Join(destDir, "footerperm"))
	require.NoError(t, err)
	require.Equal(t, "0755\n", string(dt))
}

func testCopyInvalidChmod(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM scratch
COPY --chmod=64a foo /
`)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile("foo", []byte(`foo-contents`), 0600),
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
	require.ErrorContains(t, err, "invalid chmod parameter: '64a'. it should be octal string and between 0 and 07777")

	dockerfile = []byte(`
FROM scratch
COPY --chmod=10000 foo /
`)

	dir = integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile("foo", []byte(`foo-contents`), 0600),
	)

	c, err = client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.ErrorContains(t, err, "invalid chmod parameter: '10000'. it should be octal string and between 0 and 07777")
}

func testCopyOverrideFiles(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM scratch AS base
COPY sub sub
COPY sub sub
COPY files/foo.go dest/foo.go
COPY files/foo.go dest/foo.go
COPY files dest
`,
		`
FROM nanoserver AS base
COPY sub sub
COPY sub sub
COPY files/foo.go dest/foo.go
COPY files/foo.go dest/foo.go
COPY files dest
`,
	))

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateDir("sub", 0700),
		fstest.CreateDir("sub/dir1", 0700),
		fstest.CreateDir("sub/dir1/dir2", 0700),
		fstest.CreateFile("sub/dir1/dir2/foo", []byte(`foo-contents`), 0600),
		fstest.CreateDir("files", 0700),
		fstest.CreateFile("files/foo.go", []byte(`foo.go-contents`), 0600),
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

	dt, err := os.ReadFile(filepath.Join(destDir, "sub/dir1/dir2/foo"))
	require.NoError(t, err)
	require.Equal(t, "foo-contents", string(dt))

	dt, err = os.ReadFile(filepath.Join(destDir, "dest/foo.go"))
	require.NoError(t, err)
	require.Equal(t, "foo.go-contents", string(dt))
}

func testCopyVarSubstitution(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM scratch AS base
ENV FOO bar
COPY $FOO baz
`,
		`
FROM nanoserver AS base
ENV FOO bar
COPY $FOO baz
`,
	))

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile("bar", []byte(`bar-contents`), 0600),
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

	dt, err := os.ReadFile(filepath.Join(destDir, "baz"))
	require.NoError(t, err)
	require.Equal(t, "bar-contents", string(dt))
}

func testCopyWildcards(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM scratch AS base
COPY *.go /gofiles/
COPY f*.go foo2.go
COPY sub/* /subdest/
COPY sub/*/dir2/foo /subdest2/
COPY sub/*/dir2/foo /subdest3/bar
COPY . all/
COPY sub/dir1/ subdest4
COPY sub/dir1/. subdest5
COPY sub/dir1 subdest6
`,
		`
FROM nanoserver AS base
USER ContainerAdministrator
RUN mkdir \gofiles
RUN mkdir \subdest2
RUN mkdir \subdest3
COPY *.go /gofiles/
COPY f*.go foo2.go
COPY sub/* /subdest/
COPY sub/*/dir2/foo /subdest2/
COPY sub/*/dir2/foo /subdest3/bar
COPY . all/
COPY sub/dir1/ subdest4
COPY sub/dir1/. subdest5
COPY sub/dir1 subdest6
`,
	))

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile("foo.go", []byte(`foo-contents`), 0600),
		fstest.CreateFile("bar.go", []byte(`bar-contents`), 0600),
		fstest.CreateDir("sub", 0700),
		fstest.CreateDir("sub/dir1", 0700),
		fstest.CreateDir("sub/dir1/dir2", 0700),
		fstest.CreateFile("sub/dir1/dir2/foo", []byte(`foo-contents`), 0600),
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

	dt, err := os.ReadFile(filepath.Join(destDir, "gofiles/foo.go"))
	require.NoError(t, err)
	require.Equal(t, "foo-contents", string(dt))

	dt, err = os.ReadFile(filepath.Join(destDir, "gofiles/bar.go"))
	require.NoError(t, err)
	require.Equal(t, "bar-contents", string(dt))

	dt, err = os.ReadFile(filepath.Join(destDir, "foo2.go"))
	require.NoError(t, err)
	require.Equal(t, "foo-contents", string(dt))

	dt, err = os.ReadFile(filepath.Join(destDir, "subdest/dir2/foo"))
	require.NoError(t, err)
	require.Equal(t, "foo-contents", string(dt))

	dt, err = os.ReadFile(filepath.Join(destDir, "subdest2/foo"))
	require.NoError(t, err)
	require.Equal(t, "foo-contents", string(dt))

	dt, err = os.ReadFile(filepath.Join(destDir, "subdest3/bar"))
	require.NoError(t, err)
	require.Equal(t, "foo-contents", string(dt))

	dt, err = os.ReadFile(filepath.Join(destDir, "all/foo.go"))
	require.NoError(t, err)
	require.Equal(t, "foo-contents", string(dt))

	dt, err = os.ReadFile(filepath.Join(destDir, "subdest4/dir2/foo"))
	require.NoError(t, err)
	require.Equal(t, "foo-contents", string(dt))

	dt, err = os.ReadFile(filepath.Join(destDir, "subdest5/dir2/foo"))
	require.NoError(t, err)
	require.Equal(t, "foo-contents", string(dt))

	dt, err = os.ReadFile(filepath.Join(destDir, "subdest6/dir2/foo"))
	require.NoError(t, err)
	require.Equal(t, "foo-contents", string(dt))
}

func testCopyRelative(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM busybox
WORKDIR /test1
WORKDIR test2
RUN sh -c "[ "$PWD" = '/test1/test2' ]"
COPY foo ./
RUN sh -c "[ $(cat /test1/test2/foo) = 'hello' ]"
ADD foo ./bar/baz
RUN sh -c "[ $(cat /test1/test2/bar/baz) = 'hello' ]"
COPY foo ./bar/baz2
RUN sh -c "[ $(cat /test1/test2/bar/baz2) = 'hello' ]"
WORKDIR ..
COPY foo ./
RUN sh -c "[ $(cat /test1/foo) = 'hello' ]"
COPY foo /test3/
RUN sh -c "[ $(cat /test3/foo) = 'hello' ]"
WORKDIR /test4
COPY . .
RUN sh -c "[ $(cat /test4/foo) = 'hello' ]"
WORKDIR /test5/test6
COPY foo ../
RUN sh -c "[ $(cat /test5/foo) = 'hello' ]"
`,
		`
FROM nanoserver
WORKDIR /test1
WORKDIR test2
RUN if %CD% NEQ C:\test1\test2 (exit 1)
COPY foo ./
RUN for /f %i in ('type \test1\test2\foo') do (if %i NEQ hello (exit 1))
ADD foo ./bar/baz
RUN for /f %i in ('type \test1\test2\bar\baz') do (if %i NEQ hello (exit 1))
COPY foo ./bar/baz2
RUN for /f %i in ('type \test1\test2\bar\baz2') do (if %i NEQ hello (exit 1))
WORKDIR ..
COPY foo ./
RUN for /f %i in ('type \test1\foo') do (if %i NEQ hello (exit 1))
# COPY foo /test3/ # TODO -> https://github.com/moby/buildkit/issues/5249
COPY foo /test3/foo
RUN for /f %i in ('type \test3\foo') do (if %i == hello (exit 0) else (exit 1))
WORKDIR /test4
COPY . .
RUN for /f %i in ('type \test4\foo') do (if %i NEQ hello (exit 1))
WORKDIR /test5/test6
COPY foo ../
RUN for /f %i in ('type \test5\foo') do (if %i NEQ hello (exit 1))
`,
	))

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile("foo", []byte(`hello`), 0600),
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

func testCopyUnicodePath(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)
	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM alpine
COPY test-äöü.txt /
COPY test-%C3%A4%C3%B6%C3%BC.txt /
COPY test+aou.txt /
`,
		`
FROM nanoserver
COPY test-äöü.txt /
COPY test-%C3%A4%C3%B6%C3%BC.txt /
COPY test+aou.txt /
`,
	))

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile("test-äöü.txt", []byte("foo"), 0644),
		fstest.CreateFile("test-%C3%A4%C3%B6%C3%BC.txt", []byte("bar"), 0644),
		fstest.CreateFile("test+aou.txt", []byte("baz"), 0644),
	)
	destDir := integration.Tmpdir(t)

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		Exports: []client.ExportEntry{
			{
				Type:      client.ExporterLocal,
				OutputDir: destDir.Name,
			},
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir.Name, "test-äöü.txt"))
	require.NoError(t, err)
	require.Equal(t, "foo", string(dt))

	dt, err = os.ReadFile(filepath.Join(destDir.Name, "test-%C3%A4%C3%B6%C3%BC.txt"))
	require.NoError(t, err)
	require.Equal(t, "bar", string(dt))

	dt, err = os.ReadFile(filepath.Join(destDir.Name, "test+aou.txt"))
	require.NoError(t, err)
	require.Equal(t, "baz", string(dt))
}

func testChmodNonOctal(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	f := getFrontend(t, sb)

	tcases := []struct {
		src   string
		dst   string
		mode  string
		isDir bool
	}{
		{
			src:  "file",
			dst:  "f1",
			mode: "go-w",
		},
		{
			src:  "file",
			dst:  "f2",
			mode: "u=rw,g=r,o=r",
		}, {
			src:  "file",
			dst:  "f3",
			mode: "a+X",
		},
		{
			src:   "dir",
			dst:   "d1",
			mode:  "go-w",
			isDir: true,
		},
		{
			src:   "dir",
			dst:   "d2",
			mode:  "u+rw,g+r,o-x,o+w",
			isDir: true,
		},
		{
			src:   "dir",
			dst:   "d3",
			mode:  "a+X",
			isDir: true,
		},
	}

	var expectedCommands strings.Builder
	var copyCommands strings.Builder
	var verifyCommands strings.Builder

	for _, tc := range tcases {
		if tc.isDir {
			// create nested input dir because COPY copies directory contents
			expectedCommands.WriteString("RUN mkdir -p /input/dirs/")
			expectedCommands.WriteString(tc.dst)
			expectedCommands.WriteString(" && cp -a /input/")
			expectedCommands.WriteString(tc.src)
			expectedCommands.WriteString(" /input/dirs/")
			expectedCommands.WriteString(tc.dst)
			expectedCommands.WriteByte('/')
			expectedCommands.WriteString(tc.dst)
			expectedCommands.WriteByte('\n')
			expectedCommands.WriteString("RUN cp -a /input/dirs/")
			expectedCommands.WriteString(tc.dst)
			expectedCommands.WriteString("/. /expected/ && chmod ")
			expectedCommands.WriteString(tc.mode)
			expectedCommands.WriteString(" /expected/")
			expectedCommands.WriteString(tc.dst)
			expectedCommands.WriteByte('\n')
			copyCommands.WriteString("COPY --from=base --chmod=")
			copyCommands.WriteString(tc.mode)
			copyCommands.WriteString(" /input/dirs/")
			copyCommands.WriteString(tc.dst)
			copyCommands.WriteString(" /\n")
		} else {
			expectedCommands.WriteString("RUN cp -a /input/")
			expectedCommands.WriteString(tc.src)
			expectedCommands.WriteString(" /expected/")
			expectedCommands.WriteString(tc.dst)
			expectedCommands.WriteString(" && chmod ")
			expectedCommands.WriteString(tc.mode)
			expectedCommands.WriteString(" /expected/")
			expectedCommands.WriteString(tc.dst)
			expectedCommands.WriteByte('\n')
			copyCommands.WriteString("COPY --from=base --chmod=")
			copyCommands.WriteString(tc.mode)
			copyCommands.WriteString(" /input/")
			copyCommands.WriteString(tc.src)
			copyCommands.WriteString(" /")
			copyCommands.WriteString(tc.dst)
			copyCommands.WriteByte('\n')
		}
		verifyCommands.WriteString("RUN [ \"$(stat -c %A /actual/")
		verifyCommands.WriteString(tc.dst)
		verifyCommands.WriteString(")\" = \"$(stat -c %A /expected/")
		verifyCommands.WriteString(tc.dst)
		verifyCommands.WriteString(")\" ]\n")
	}

	dockerfile := fmt.Appendf(nil, `
FROM alpine as base
RUN <<eot
	set -ex
	mkdir /input
	touch /input/file
	chmod 666 /input/file
	mkdir /input/dir
	chmod 124 /input/dir
	mkdir /expected
eot
%s

FROM scratch as result
%s

FROM base
COPY --from=result / /actual/
%s

`, expectedCommands.String(), copyCommands.String(), verifyCommands.String())

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
