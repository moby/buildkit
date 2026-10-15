package dockerfile

import (
	"archive/tar"
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/util/testutil/httpserver"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	"github.com/tonistiigi/fsutil"
)

// testDockerfileFromGitSHA1 runs testDockerfileFromGit against a repo using
// the SHA-1 object format.
func testDockerfileFromGitSHA1(t *testing.T, sb integration.Sandbox) {
	testDockerfileFromGit(t, sb, "sha1")
}

// testDockerfileFromGitSHA256 runs testDockerfileFromGit against a repo using
// the SHA-256 object format.
func testDockerfileFromGitSHA256(t *testing.T, sb integration.Sandbox) {
	testDockerfileFromGit(t, sb, "sha256")
}

// testDockerfileFromGit verifies using a Git repo served over HTTP as the
// build context. It serves a local repo (sha1 or sha256) with two commits and
// solves twice: once pinned to branch "first" (expects only `bar`) and once
// on the default branch (expects both `bar` and `bar2`), covering ref
// resolution and URL-fragment branch selection for both hash formats.
func testDockerfileFromGit(t *testing.T, sb integration.Sandbox, format string) {
	// Skipped on Windows:
	// BuildKit's Git source handler fails to update submodules on Windows even when
	// the repo has no submodules. The error "failed to update submodules ... git stderr:"
	// occurs with empty stderr, indicating the submodule update command cannot execute
	// properly on Windows.
	integration.SkipOnPlatform(t, "windows", "Git source handler submodule update not supported on Windows")
	f := getFrontend(t, sb)

	gitDir := t.TempDir()

	dockerfile := `
FROM busybox AS build
RUN echo -n fromgit > foo
FROM scratch
COPY --from=build foo bar
`

	err := os.WriteFile(filepath.Join(gitDir, "Dockerfile"), []byte(dockerfile), 0600)
	require.NoError(t, err)

	initOptions := ""
	if format == "sha256" {
		initOptions = " --object-format=sha256"
	}
	err = runShell(gitDir,
		"git init"+initOptions,
		"git config --local user.email test",
		"git config --local user.name test",
		"git add Dockerfile",
		"git commit -m initial",
		"git branch first",
	)
	require.NoError(t, err)

	dockerfile += `
COPY --from=build foo bar2
`

	err = os.WriteFile(filepath.Join(gitDir, "Dockerfile"), []byte(dockerfile), 0600)
	require.NoError(t, err)

	err = runShell(gitDir,
		"git add Dockerfile",
		"git commit -m second",
		"git update-server-info",
	)
	require.NoError(t, err)

	server := httptest.NewServer(http.FileServer(http.Dir(filepath.Clean(gitDir))))
	defer server.Close()

	destDir := t.TempDir()

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"context": server.URL + "/.git#first",
		},
		Exports: []client.ExportEntry{
			{
				Type:      client.ExporterLocal,
				OutputDir: destDir,
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "bar"))
	require.NoError(t, err)
	require.Equal(t, "fromgit", string(dt))

	_, err = os.Stat(filepath.Join(destDir, "bar2"))
	require.Error(t, err)
	require.True(t, errors.Is(err, os.ErrNotExist))

	// second request from master branch contains both files
	destDir = t.TempDir()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"context": server.URL + "/.git",
		},
		Exports: []client.ExportEntry{
			{
				Type:      client.ExporterLocal,
				OutputDir: destDir,
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err = os.ReadFile(filepath.Join(destDir, "bar"))
	require.NoError(t, err)
	require.Equal(t, "fromgit", string(dt))

	dt, err = os.ReadFile(filepath.Join(destDir, "bar2"))
	require.NoError(t, err)
	require.Equal(t, "fromgit", string(dt))
}

func testDockerfileGitTagsCache(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows", "Git source submodule update not supported on Windows")
	f := getFrontend(t, sb)
	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	for _, mode := range []string{"context", "add"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\nCOPY . /\n"), 0600))
			require.NoError(t, runShell(dir,
				"git init -b master",
				"git config user.email test@example.com",
				"git config user.name test",
				"git add Dockerfile",
				"git commit -m initial",
				"git tag v1.0.0",
				"git commit --allow-empty -m second",
				"git update-server-info",
			))
			server := httptest.NewServer(http.FileServer(http.Dir(dir)))
			defer server.Close()
			url := server.URL + "/.git?fetch-depth=0"
			opt := client.SolveOpt{FrontendAttrs: map[string]string{
				"context": url,
				"build-arg:BUILDKIT_CONTEXT_KEEP_GIT_DIR": "1",
			}}
			if mode == "add" {
				dockerfileDir := t.TempDir()
				require.NoError(t, os.WriteFile(filepath.Join(dockerfileDir, "Dockerfile"), fmt.Appendf(nil, "FROM scratch\nADD --keep-git-dir %s /\n", url), 0600))
				fs, err := fsutil.NewFS(dockerfileDir)
				require.NoError(t, err)
				opt.FrontendAttrs = nil
				opt.LocalMounts = map[string]fsutil.FS{"dockerfile": fs, "context": fs}
			}
			for i, version := range []string{"v1.0.0-1-g", "v1.1.0-0-g", "v1.0.0-1-g"} {
				switch i {
				case 1:
					require.NoError(t, runShell(dir, "git tag v1.1.0", "git update-server-info"))
				case 2:
					require.NoError(t, runShell(dir, "git tag -d v1.1.0", "git update-server-info"))
				}
				outDir := t.TempDir()
				opt.Exports = []client.ExportEntry{{Type: client.ExporterLocal, OutputDir: outDir}}
				_, err := f.Solve(sb.Context(), c, opt, nil)
				require.NoError(t, err)
				cmd := exec.CommandContext(t.Context(), "git", "describe", "--tags", "--long")
				cmd.Dir = outDir
				out, err := cmd.CombinedOutput()
				require.NoError(t, err, "%s", out)
				require.True(t, strings.HasPrefix(strings.TrimSpace(string(out)), version), "%s", out)
			}
		})
	}
}

func testDockerfileGitFetchDepth(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows", "Git source not supported on Windows")
	f := getFrontend(t, sb)
	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\nCOPY . /\n"), 0600))
	require.NoError(t, runShell(dir,
		"git init -b main",
		"git config user.email test@example.com",
		"git config user.name test",
		"git add Dockerfile",
		"git commit -m initial",
		"git commit --allow-empty -m second",
		"git commit --allow-empty -m third",
		"git update-server-info",
	))
	// Dumb HTTP cannot serve shallow fetches. The remote fallback must fetch
	// full history, while the final retained checkout still honors depth two.
	server := httptest.NewServer(http.FileServer(http.Dir(dir)))
	defer server.Close()
	url := server.URL + "/.git?fetch-depth=2"
	for _, mode := range []string{"context", "add"} {
		t.Run(mode, func(t *testing.T) {
			opt := client.SolveOpt{FrontendAttrs: map[string]string{
				"context": url,
				"build-arg:BUILDKIT_CONTEXT_KEEP_GIT_DIR": "1",
			}}
			if mode == "add" {
				dockerfileDir := t.TempDir()
				require.NoError(t, os.WriteFile(filepath.Join(dockerfileDir, "Dockerfile"), fmt.Appendf(nil, "FROM scratch\nADD --keep-git-dir %s /\n", url), 0600))
				fs, err := fsutil.NewFS(dockerfileDir)
				require.NoError(t, err)
				opt.FrontendAttrs = nil
				opt.LocalMounts = map[string]fsutil.FS{"dockerfile": fs, "context": fs}
			}
			outDir := t.TempDir()
			opt.Exports = []client.ExportEntry{{Type: client.ExporterLocal, OutputDir: outDir}}
			_, err := f.Solve(sb.Context(), c, opt, nil)
			require.NoError(t, err)
			cmd := exec.CommandContext(t.Context(), "git", "-C", outDir, "rev-list", "--count", "HEAD")
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", out)
			require.Equal(t, "2", strings.TrimSpace(string(out)))
		})
	}
}

func testDockerfileFromHTTP(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	buf := bytes.NewBuffer(nil)
	w := tar.NewWriter(buf)

	writeFile := func(fn, dt string) {
		err := w.WriteHeader(&tar.Header{
			Name:     fn,
			Mode:     0600,
			Size:     int64(len(dt)),
			Typeflag: tar.TypeReg,
		})
		require.NoError(t, err)
		_, err = w.Write([]byte(dt))
		require.NoError(t, err)
	}

	dockerfile := fmt.Sprintf(`FROM %s
COPY foo bar
`, integration.UnixOrWindows("scratch", "nanoserver"))
	writeFile("mydockerfile", dockerfile)

	writeFile("foo", "foo-contents")

	require.NoError(t, w.Flush())

	resp := &httpserver.Response{
		Etag:    identity.NewID(),
		Content: buf.Bytes(),
	}

	server := httpserver.NewTestServer(map[string]*httpserver.Response{
		"/myurl": resp,
	})
	defer server.Close()

	destDir := t.TempDir()

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"context":  server.URL + "/myurl",
			"filename": "mydockerfile",
		},
		Exports: []client.ExportEntry{
			{
				Type:      client.ExporterLocal,
				OutputDir: destDir,
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "bar"))
	require.NoError(t, err)
	require.Equal(t, "foo-contents", string(dt))
}

func testHTTPDockerfile(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM busybox
RUN echo -n "foo-contents" > /foo
FROM scratch
COPY --from=0 /foo /foo
`,
		`
FROM nanoserver
USER ContainerAdministrator
RUN echo foo-contents> /foo
`,
	))

	srcDir := t.TempDir()

	err := os.WriteFile(filepath.Join(srcDir, "Dockerfile"), dockerfile, 0600)
	require.NoError(t, err)

	resp := &httpserver.Response{
		Etag:    identity.NewID(),
		Content: dockerfile,
	}

	server := httpserver.NewTestServer(map[string]*httpserver.Response{
		"/df": resp,
	})
	defer server.Close()

	destDir := t.TempDir()

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"context":  server.URL + "/df",
			"filename": "mydockerfile", // this is bogus, any name should work
		},
		Exports: []client.ExportEntry{
			{
				Type:      client.ExporterLocal,
				OutputDir: destDir,
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "foo"))
	require.NoError(t, err)
	foo := integration.UnixOrWindows(
		"foo-contents",
		"foo-contents\r\n", // Windows echo command adds \r\n
	)
	require.Equal(t, foo, string(dt))
}
