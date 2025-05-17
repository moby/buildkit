package dockerfile

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"text/template"

	"github.com/containerd/continuity/fs/fstest"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/frontend/dockerui"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/stretchr/testify/require"
	"github.com/tonistiigi/fsutil"
)

var addGitTests = integration.TestFuncs(
	testAddGit,
	testAddGitChecksumCache,
)

func init() {
	allTests = append(allTests, addGitTests...)
}

func testAddGit(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	f := getFrontend(t, sb)

	gitDir, err := os.MkdirTemp("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(gitDir)
	gitCommands := []string{
		"git init",
		"git config --local user.email test",
		"git config --local user.name test",
	}
	makeCommit := func(tag string) []string {
		return []string{
			"echo foo of " + tag + " >foo",
			"git add foo",
			"git commit -m " + tag,
			"git tag " + tag,
		}
	}
	gitCommands = append(gitCommands, makeCommit("v0.0.1")...)
	gitCommands = append(gitCommands, makeCommit("v0.0.2")...)
	gitCommands = append(gitCommands, makeCommit("v0.0.3")...)
	gitCommands = append(gitCommands, "git update-server-info")
	err = runShell(gitDir, gitCommands...)
	require.NoError(t, err)

	revParseCmd := exec.Command("git", "rev-parse", "v0.0.2")
	revParseCmd.Dir = gitDir
	commitHashB, err := revParseCmd.Output()
	require.NoError(t, err)
	commitHashV2 := strings.TrimSpace(string(commitHashB))

	revParseCmd = exec.Command("git", "rev-parse", "v0.0.3")
	revParseCmd.Dir = gitDir
	commitHashB, err = revParseCmd.Output()
	require.NoError(t, err)
	commitHashV3 := strings.TrimSpace(string(commitHashB))

	server := httptest.NewServer(http.FileServer(http.Dir(filepath.Clean(gitDir))))
	defer server.Close()
	serverURL := server.URL
	t.Logf("serverURL=%q", serverURL)

	dockerfile, err := applyTemplate(`
FROM alpine

# Basic case
ADD {{.ServerURL}}/.git#v0.0.1 /x
RUN cd /x && \
  [ "$(cat foo)" = "foo of v0.0.1" ]

# Complicated case
ARG REPO="{{.ServerURL}}/.git"
ARG TAG="v0.0.2"
ADD --keep-git-dir=true --chown=4242:8484 --checksum={{.Checksum}} ${REPO}#${TAG} /buildkit-chowned
RUN apk add git
USER 4242
RUN cd /buildkit-chowned && \
  [ "$(cat foo)" = "foo of v0.0.2" ] && \
  [ "$(stat -c %u foo)" = "4242" ] && \
  [ "$(stat -c %g foo)" = "8484" ] && \
  [ -z "$(git status -s)" ]
`, map[string]string{
		"ServerURL": serverURL,
		"Checksum":  commitHashV2,
	})
	require.NoError(t, err)

	dir := integration.Tmpdir(t,
		fstest.CreateFile("Dockerfile", []byte(dockerfile), 0600),
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

	// Additional test: ADD from Git URL with checksum but without keep-git-dir flag
	dockerfile2, err := applyTemplate(`
FROM alpine
ARG REPO="{{.ServerURL}}/.git"
ARG TAG="v0.0.3"
ADD --checksum={{.Checksum}} ${REPO}#${TAG} /nogitdir
RUN [ -f /nogitdir/foo ]
RUN [ "$(cat /nogitdir/foo)" = "foo of v0.0.3" ]
RUN [ ! -d /nogitdir/.git ]
`, map[string]string{
		"ServerURL": serverURL,
		"Checksum":  commitHashV3,
	})
	require.NoError(t, err)

	dir2 := integration.Tmpdir(t,
		fstest.CreateFile("Dockerfile", []byte(dockerfile2), 0600),
	)

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir2,
			dockerui.DefaultLocalNameContext:    dir2,
		},
	}, nil)
	require.NoError(t, err)

	// access initial ref again that was already pulled
	dockerfile3, err := applyTemplate(`
		FROM alpine
		ARG REPO="{{.ServerURL}}/.git"
		ARG TAG="v0.0.2"
		ADD --keep-git-dir --checksum={{.Checksum}} ${REPO}#${TAG} /nogitdir
		RUN [ -f /nogitdir/foo ]
		RUN [ "$(cat /nogitdir/foo)" = "foo of v0.0.2" ]
		RUN [ -d /nogitdir/.git ]
		`, map[string]string{
		"ServerURL": serverURL,
		"Checksum":  commitHashV2,
	})
	require.NoError(t, err)

	dir3 := integration.Tmpdir(t,
		fstest.CreateFile("Dockerfile", []byte(dockerfile3), 0600),
	)

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir3,
			dockerui.DefaultLocalNameContext:    dir3,
		},
	}, nil)
	require.NoError(t, err)

	// Additional test: ADD from Git URL using commitHashV3 for both checksum and ref
	dockerfile4, err := applyTemplate(`
	FROM alpine
	ARG REPO="{{.ServerURL}}/.git"
	ARG COMMIT="{{.Checksum}}"
	ADD --keep-git-dir=true --checksum={{.Checksum}} ${REPO}#${COMMIT} /commitdir
	RUN [ -f /commitdir/foo ]
	RUN [ "$(cat /commitdir/foo)" = "foo of v0.0.3" ]
	RUN [ -d /commitdir/.git ]
	`, map[string]string{
		"ServerURL": serverURL,
		"Checksum":  commitHashV3,
	})
	require.NoError(t, err)

	dir4 := integration.Tmpdir(t,
		fstest.CreateFile("Dockerfile", []byte(dockerfile4), 0600),
	)

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir4,
			dockerui.DefaultLocalNameContext:    dir4,
		},
	}, nil)
	require.NoError(t, err)

	// checksum does not match
	dockerfile5, err := applyTemplate(`
	FROM alpine
	ARG REPO="{{.ServerURL}}/.git"
	ARG TAG="v0.0.3"
	ADD --checksum={{.WrongChecksum}} ${REPO}#${TAG} /faildir
	`, map[string]string{
		"ServerURL":     serverURL,
		"WrongChecksum": commitHashV2, // v0.0.2 hash, but ref is v0.0.3
	})
	require.NoError(t, err)

	dir5 := integration.Tmpdir(t,
		fstest.CreateFile("Dockerfile", []byte(dockerfile5), 0600),
	)

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir5,
			dockerui.DefaultLocalNameContext:    dir5,
		},
	}, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "expected checksum to match")

	//  checksum is garbage
	dockerfile6, err := applyTemplate(`
	FROM alpine
	ARG REPO="{{.ServerURL}}/.git"
	ARG TAG="v0.0.3"
	ADD --checksum=foobar ${REPO}#${TAG} /faildir
	`, map[string]string{
		"ServerURL": serverURL,
	})
	require.NoError(t, err)

	dir6 := integration.Tmpdir(t,
		fstest.CreateFile("Dockerfile", []byte(dockerfile6), 0600),
	)

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir6,
			dockerui.DefaultLocalNameContext:    dir6,
		},
	}, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid checksum")
	require.Contains(t, err.Error(), "expected hex commit hash")
}

func testAddGitChecksumCache(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	f := getFrontend(t, sb)

	gitDir, err := os.MkdirTemp("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(gitDir)
	gitCommands := []string{
		"git init",
		"git config --local user.email test",
		"git config --local user.name test",
	}
	makeCommit := func(tag string) []string {
		return []string{
			"echo foo of " + tag + " >foo",
			"git add foo",
			"git commit -m " + tag,
			"git tag " + tag,
		}
	}
	gitCommands = append(gitCommands, makeCommit("v0.0.1")...)
	gitCommands = append(gitCommands, makeCommit("v0.0.2")...)
	gitCommands = append(gitCommands, "git update-server-info")
	err = runShell(gitDir, gitCommands...)
	require.NoError(t, err)

	revParseCmd := exec.Command("git", "rev-parse", "v0.0.2")
	revParseCmd.Dir = gitDir
	commitHashB, err := revParseCmd.Output()
	require.NoError(t, err)
	commitHash := strings.TrimSpace(string(commitHashB))

	server := httptest.NewServer(http.FileServer(http.Dir(filepath.Clean(gitDir))))
	defer server.Close()
	serverURL := server.URL

	// First build: without checksum, from tag, generate unique.txt from /dev/urandom and copy to scratch
	dockerfile1 := `
FROM alpine AS src
ADD --keep-git-dir ` + serverURL + `/.git#v0.0.2 /repo
RUN head -c 16 /dev/urandom | base64 > /repo/unique.txt

FROM scratch
COPY --from=src /repo/unique.txt /
`
	dir1 := integration.Tmpdir(t,
		fstest.CreateFile("Dockerfile", []byte(dockerfile1), 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	destDir1 := t.TempDir()
	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		Exports: []client.ExportEntry{
			{
				Type:      client.ExporterLocal,
				OutputDir: destDir1,
			},
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir1,
			dockerui.DefaultLocalNameContext:    dir1,
		},
	}, nil)
	require.NoError(t, err)

	unique1, err := os.ReadFile(filepath.Join(destDir1, "unique.txt"))
	require.NoError(t, err)

	// Second build: with checksum, should match cache even though this one sets commitHash and get same unique.txt
	dockerfile2 := `
FROM alpine AS src
ADD --keep-git-dir --checksum=` + commitHash + ` ` + serverURL + `/.git#v0.0.2 /repo
RUN head -c 16 /dev/urandom | base64 > /repo/unique.txt

FROM scratch
COPY --from=src /repo/unique.txt /
`
	dir2 := integration.Tmpdir(t,
		fstest.CreateFile("Dockerfile", []byte(dockerfile2), 0600),
	)

	destDir2 := t.TempDir()
	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		Exports: []client.ExportEntry{
			{
				Type:      client.ExporterLocal,
				OutputDir: destDir2,
			},
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir2,
			dockerui.DefaultLocalNameContext:    dir2,
		},
	}, nil)
	require.NoError(t, err)

	unique2, err := os.ReadFile(filepath.Join(destDir2, "unique.txt"))
	require.NoError(t, err)

	require.Equal(t, string(unique1), string(unique2), "cache should be matched and unique file content should be the same")
}

func applyTemplate(tmpl string, x any) (string, error) {
	var buf bytes.Buffer
	parsed, err := template.New("").Parse(tmpl)
	if err != nil {
		return "", err
	}
	if err := parsed.Execute(&buf, x); err != nil {
		return "", err
	}
	return buf.String(), nil
}
