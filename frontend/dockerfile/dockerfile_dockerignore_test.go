package dockerfile

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/containerd/continuity/fs/fstest"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/frontend/dockerui"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	"github.com/tonistiigi/fsutil"
)

func testDockerignore(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM scratch
COPY . .
`,
		`
FROM nanoserver
COPY . .
`,
	))

	dockerignore := []byte(`
ba*
Dockerfile
!bay
.dockerignore
`)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile("foo", []byte(`foo-contents`), 0600),
		fstest.CreateFile("bar", []byte(`bar-contents`), 0600),
		fstest.CreateFile("baz", []byte(`baz-contents`), 0600),
		fstest.CreateFile("bay", []byte(`bay-contents`), 0600),
		fstest.CreateFile(".dockerignore", dockerignore, 0600),
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
	require.Equal(t, "foo-contents", string(dt))

	_, err = os.Stat(filepath.Join(destDir, ".dockerignore"))
	require.Error(t, err)
	require.True(t, errors.Is(err, os.ErrNotExist))

	_, err = os.Stat(filepath.Join(destDir, "Dockerfile"))
	require.Error(t, err)
	require.True(t, errors.Is(err, os.ErrNotExist))

	_, err = os.Stat(filepath.Join(destDir, "bar"))
	require.Error(t, err)
	require.True(t, errors.Is(err, os.ErrNotExist))

	_, err = os.Stat(filepath.Join(destDir, "baz"))
	require.Error(t, err)
	require.True(t, errors.Is(err, os.ErrNotExist))

	dt, err = os.ReadFile(filepath.Join(destDir, "bay"))
	require.NoError(t, err)
	require.Equal(t, "bay-contents", string(dt))
}

func testDockerignoreInvalid(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM scratch
COPY . .
`,
		`
FROM nanoserver
COPY . .
`,
	))

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile(".dockerignore", []byte("!\n"), 0600),
	)

	ctx, cancel := context.WithCancelCause(sb.Context())
	ctx, _ = context.WithTimeoutCause(ctx, 15*time.Second, errors.WithStack(context.DeadlineExceeded)) //nolint:govet
	defer func() { cancel(errors.WithStack(context.Canceled)) }()

	c, err := client.New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	_, err = f.Solve(ctx, c, client.SolveOpt{
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	// err is either the expected error due to invalid dockerignore or error from the timeout
	require.Error(t, err)
	select {
	case <-ctx.Done():
		t.Fatal("timed out")
	default:
	}
}

func testDockerignoreOverride(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)
	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM busybox
COPY . .
RUN [ -f foo ] && [ ! -f bar ]
`,
		`
FROM nanoserver
COPY . .
RUN if exist foo (if not exist bar (exit 0) else (exit 1))
`,
	))

	ignore := []byte(`
bar
`)

	dockerfile2 := []byte(integration.UnixOrWindows(
		`
FROM busybox
COPY . .
RUN [ ! -f foo ] && [ -f bar ]
`,
		`
FROM nanoserver
COPY . .
RUN if not exist foo (if exist bar (exit 0) else (exit 1))
`,
	))

	ignore2 := []byte(`
foo
`)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile("Dockerfile.dockerignore", ignore, 0600),
		fstest.CreateFile("Dockerfile2", dockerfile2, 0600),
		fstest.CreateFile("Dockerfile2.dockerignore", ignore2, 0600),
		fstest.CreateFile("foo", []byte("contents0"), 0600),
		fstest.CreateFile("bar", []byte("contents0"), 0600),
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
			"filename": "Dockerfile2",
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)
}
