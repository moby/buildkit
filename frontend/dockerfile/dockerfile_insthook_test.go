package dockerfile

import (
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

var instHookTests = integration.TestFuncs(
	testInstructionHook,
)

func testInstructionHook(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM busybox AS base
RUN echo "$FOO" >/foo

FROM scratch
COPY --from=base /foo /foo
`)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)
	destDir := t.TempDir()

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	build := func(attrs map[string]string) string {
		_, err = f.Solve(sb.Context(), c, client.SolveOpt{
			FrontendAttrs: attrs,
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
		p := filepath.Join(destDir, "foo")
		b, err := os.ReadFile(p)
		require.NoError(t, err)
		return strings.TrimSpace(string(b))
	}

	require.Equal(t, "", build(nil))

	const hook = `
{
  "RUN": {
    "entrypoint": ["/dev/.dfhook/bin/busybox", "env", "FOO=BAR"],
    "mounts": [
      {"from": "busybox:uclibc", "target": "/dev/.dfhook"}
    ]
  }
}`
	require.Equal(t, "BAR", build(map[string]string{"hook": hook}))
}
