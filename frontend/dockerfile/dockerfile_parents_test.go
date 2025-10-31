package dockerfile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/containerd/continuity/fs/fstest"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/frontend/dockerui"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/stretchr/testify/require"
	"github.com/tonistiigi/fsutil"
)

var parentsTests = integration.TestFuncs(
	testCopyParents,
	testCopyRelativeParents,
	// Duplicate of testCopyRelativeParents but more simplified
	// so it will run on Windows. Remove when testCopyRelativeParents
	// works on Windows.
	testCopyRelativeParentsSimplified,
	testCopyParentsMissingDirectory,
)

func init() {
	allTests = append(allTests, parentsTests...)
}

func testCopyParents(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM scratch
COPY --parents foo1/foo2/bar /

WORKDIR /test
COPY --parents foo1/foo2/ba* .
`)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateDir("foo1", 0700),
		fstest.CreateDir("foo1/foo2", 0700),
		fstest.CreateFile("foo1/foo2/bar", []byte(`testing`), 0600),
		fstest.CreateFile("foo1/foo2/baz", []byte(`testing2`), 0600),
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

	dt, err := os.ReadFile(filepath.Join(destDir, "foo1/foo2/bar"))
	require.NoError(t, err)
	require.Equal(t, "testing", string(dt))

	dt, err = os.ReadFile(filepath.Join(destDir, "test/foo1/foo2/bar"))
	require.NoError(t, err)
	require.Equal(t, "testing", string(dt))
	dt, err = os.ReadFile(filepath.Join(destDir, "test/foo1/foo2/baz"))
	require.NoError(t, err)
	require.Equal(t, "testing2", string(dt))
}

func testCopyRelativeParents(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM alpine AS base
WORKDIR /test
RUN <<eot
	set -ex
	mkdir -p a/b/c/d/e
	mkdir -p a/b2/c/d/e
	mkdir -p a/b/c2/d/e
	mkdir -p a/b/c2/d/e2
	touch a/b/c/d/foo
	touch a/b/c/d/e/bay
	touch a/b2/c/d/e/bar
	touch a/b/c2/d/e/baz
	touch a/b/c2/d/e2/baz
eot

FROM alpine AS middle
COPY --from=base --parents /test/a/b/./c/d /out/
RUN <<eot
	set -ex
	[ -d /out/c/d/e ]
	[ -f /out/c/d/foo ]
	[ ! -d /out/a ]
	[ ! -d /out/e ]
eot

FROM alpine AS end
COPY --from=base --parents /test/a/b/c/d/. /out/
RUN <<eot
	set -ex
	[ -d /out/test/a/b/c/d/e ]
	[ -f /out/test/a/b/c/d/foo ]
eot

FROM alpine AS start
COPY --from=base --parents ./test/a/b/c/d /out/
RUN <<eot
	set -ex
	[ -d /out/test/a/b/c/d/e ]
	[ -f /out/test/a/b/c/d/foo ]
eot

FROM alpine AS double
COPY --from=base --parents /test/a/./b/./c /out/
RUN <<eot
	set -ex
	[ -d /out/b/c/d/e ]
	[ -f /out/b/c/d/foo ]
eot

FROM alpine AS wildcard
COPY --from=base --parents /test/a/./*/c /out/
RUN <<eot
	set -ex
	[ -d /out/b/c/d/e ]
	[ -f /out/b2/c/d/e/bar ]
eot

FROM alpine AS doublewildcard
COPY --from=base --parents /test/a/b*/./c/**/e /out/
RUN <<eot
	set -ex
	[ -d /out/c/d/e ]
	[ -f /out/c/d/e/bay ] # via b
	[ -f /out/c/d/e/bar ] # via b2
eot

FROM alpine AS doubleinputs
COPY --from=base --parents /test/a/b/c*/./d/**/baz /test/a/b*/./c/**/bar /out/
RUN <<eot
	set -ex
	[ -f /out/d/e/baz ]
	[ ! -f /out/d/e/bay ]
	[ -f /out/d/e2/baz ]
	[ -f /out/c/d/e/bar ] # via b2
eot
`)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	for _, target := range []string{"middle", "end", "start", "double", "wildcard", "doublewildcard", "doubleinputs"} {
		t.Logf("target: %s", target)
		_, err = f.Solve(sb.Context(), c, client.SolveOpt{
			FrontendAttrs: map[string]string{
				"target": target,
			},
			LocalMounts: map[string]fsutil.FS{
				dockerui.DefaultLocalNameDockerfile: dir,
				dockerui.DefaultLocalNameContext:    dir,
			},
		}, nil)
		require.NoError(t, err)
	}
}

func testCopyRelativeParentsSimplified(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(integration.UnixOrWindows(`
FROM alpine AS base
WORKDIR /test
RUN <<eot
	set -ex
	mkdir -p a/b/c/d/e
	mkdir -p a/b2/c/d/e
	mkdir -p a/b/c2/d/e
	mkdir -p a/b/c2/d/e2
	touch a/b/c/d/foo
	touch a/b/c/d/e/bay
	touch a/b2/c/d/e/bar
	touch a/b/c2/d/e/baz
	touch a/b/c2/d/e2/baz
eot

FROM alpine AS middle
COPY --from=base --parents /test/a/b/./c/d /out/
RUN <<eot
	set -ex
	[ -d /out/c/d/e ]
	[ -f /out/c/d/foo ]
	[ ! -d /out/a ]
	[ ! -d /out/e ]
eot

FROM alpine AS end
COPY --from=base --parents /test/a/b/c/d/. /out/
RUN <<eot
	set -ex
	[ -d /out/test/a/b/c/d/e ]
	[ -f /out/test/a/b/c/d/foo ]
eot

FROM alpine AS start
COPY --from=base --parents ./test/a/b/c/d /out/
RUN <<eot
	set -ex
	[ -d /out/test/a/b/c/d/e ]
	[ -f /out/test/a/b/c/d/foo ]
eot

FROM alpine AS double
COPY --from=base --parents /test/a/./b/./c /out/
RUN <<eot
	set -ex
	[ -d /out/b/c/d/e ]
	[ -f /out/b/c/d/foo ]
eot

FROM alpine AS wildcard
COPY --from=base --parents /test/a/./*/c /out/
RUN <<eot
	set -ex
	[ -d /out/b/c/d/e ]
	[ -f /out/b2/c/d/e/bar ]
eot

FROM alpine AS doublewildcard
COPY --from=base --parents /test/a/b*/./c/**/e /out/
RUN <<eot
	set -ex
	[ -d /out/c/d/e ]
	[ -f /out/c/d/e/bay ] # via b
	[ -f /out/c/d/e/bar ] # via b2
eot

FROM alpine AS doubleinputs
COPY --from=base --parents /test/a/b/c*/./d/**/baz /test/a/b*/./c/**/bar /out/
RUN <<eot
	set -ex
	[ -f /out/d/e/baz ]
	[ ! -f /out/d/e/bay ]
	[ -f /out/d/e2/baz ]
	[ -f /out/c/d/e/bar ] # via b2
eot
`, `
FROM nanoserver AS base
USER ContainerAdministrator
WORKDIR /test
RUN mkdir a\b\c\d\e && mkdir a\b2\c\d\e && mkdir a\b\c2\d\e && mkdir a\b\c2\d\e2 && type nul > a\b\c\d\foo && type nul > a\b\c\d\e\bay && type nul > a\b2\c\d\e\bar && type nul > a\b\c2\d\e\baz && type nul > a\b\c2\d\e2\baz

FROM nanoserver AS middle
USER ContainerAdministrator
COPY --from=base --parents /test/a/b/./c/d /out/
RUN if not exist \out\c\d\e exit 1
RUN if not exist \out\c\d\foo exit 1
RUN if exist \out\a exit 1
RUN if exist \out\e exit 1

FROM nanoserver AS end
USER ContainerAdministrator
COPY --from=base --parents /test/a/b/c/d/. /out/
RUN if not exist \out\test\a\b\c\d\e exit 1
RUN if not exist \out\test\a\b\c\d\foo exit 1

FROM nanoserver AS start
USER ContainerAdministrator
COPY --from=base --parents ./test/a/b/c/d /out/
RUN if not exist \out\test\a\b\c\d\e exit 1
RUN if not exist \out\test\a\b\c\d\foo exit 1

FROM nanoserver AS double
USER ContainerAdministrator
COPY --from=base --parents /test/a/./b/./c /out/
RUN if not exist \out\b\c\d\e exit 1
RUN if not exist \out\b\c\d\foo exit 1

FROM nanoserver AS wildcard
USER ContainerAdministrator
COPY --from=base --parents /test/a/./*/c /out/
RUN if not exist \out\b\c\d\e exit 1
RUN if not exist \out\b2\c\d\e\bar exit 1

FROM nanoserver AS doublewildcard
USER ContainerAdministrator
COPY --from=base --parents /test/a/b*/./c/**/e /out/
RUN if not exist \out\c\d\e exit 1
RUN if not exist \out\c\d\e\bay exit 1
RUN if not exist \out\c\d\e\bar exit 1

FROM nanoserver AS doubleinputs
USER ContainerAdministrator
COPY --from=base --parents /test/a/b/c*/./d/**/baz /test/a/b*/./c/**/bar /out/
RUN if not exist \out\d\e\baz exit 1
RUN if exist \out\d\e\bay exit 1
RUN if not exist \out\d\e2\baz exit 1
RUN if not exist \out\c\d\e\bar exit 1
`))

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	for _, target := range []string{"middle", "end", "start", "double", "wildcard", "doublewildcard", "doubleinputs"} {
		t.Logf("target: %s", target)
		_, err = f.Solve(sb.Context(), c, client.SolveOpt{
			FrontendAttrs: map[string]string{
				"target": target,
			},
			LocalMounts: map[string]fsutil.FS{
				dockerui.DefaultLocalNameDockerfile: dir,
				dockerui.DefaultLocalNameContext:    dir,
			},
		}, nil)
		require.NoError(t, err)
	}
}

func testCopyParentsMissingDirectory(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM alpine AS base
WORKDIR /test
RUN <<eot
	set -ex
	mkdir -p a/b/c/d/e
	touch a/b/c/d/foo
	touch a/b/c/d/e/bay
eot

FROM alpine AS normal
COPY --from=base --parents /test/a/b/c/d /out/
RUN <<eot
	set -ex
	[ -d /out/test/a/b/c/d/e ]
	[ -f /out/test/a/b/c/d/e/bay ]
	[ ! -d /out/e ]
	[ ! -d /out/a ]
eot

FROM alpine AS withpivot
COPY --from=base --parents /test/a/b/./c/d /out/
RUN <<eot
	set -ex
	[ -d /out/c/d/e ]
	[ -f /out/c/d/foo ]
	[ ! -d /out/a ]
	[ ! -d /out/e ]
eot

FROM alpine AS nonexistentfile
COPY --from=base --parents /test/nonexistent-file /out/

FROM alpine AS wildcard-nonexistent
COPY --from=base --parents /test/a/b2*/c /out/
RUN <<eot
	set -ex
	[ -d /out ]
	[ ! -d /out/a ]
eot

FROM alpine AS wildcard-afterpivot
COPY --from=base --parents /test/a/b/./c2* /out/
RUN <<eot
	set -ex
	[ -d /out ]
	[ ! -d /out/a ]
	[ ! -d /out/c* ]
eot
`)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	type test struct {
		target     string
		errorRegex any
	}

	tests := []test{
		{"normal", nil},
		{"withpivot", nil},
		{"nonexistentfile", `failed to calculate checksum of ref.*: "/test/nonexistent-file": not found`},
		{"wildcard-nonexistent", nil},
		{"wildcard-afterpivot", nil},
	}

	for _, tt := range tests {
		t.Logf("target: %s", tt.target)
		_, err = f.Solve(sb.Context(), c, client.SolveOpt{
			FrontendAttrs: map[string]string{
				"target": tt.target,
			},
			LocalMounts: map[string]fsutil.FS{
				dockerui.DefaultLocalNameDockerfile: dir,
				dockerui.DefaultLocalNameContext:    dir,
			},
		}, nil)

		if tt.errorRegex != nil {
			require.Error(t, err)
			require.Regexp(t, tt.errorRegex, err.Error())
		} else {
			require.NoError(t, err)
		}
	}
}
