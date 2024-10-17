//go:build windows
// +build windows

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

var windowsTests = integration.TestFuncs(
	testSetPath,
)

func init() {
	allTests = append(allTests, windowsTests...)
}

func testSetPath(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM nanoserver
RUN setx PATH "C:\NewPath1;C:\NewPath2;%PATH%"
RUN echo %PATH% > path.txt
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

	dt, err := os.ReadFile(filepath.Join(destDir, "path.txt"))
	require.NoError(t, err)

	envStr := string(dt)
	require.Contains(t, envStr, "C:\\NewPath1;C:\\NewPath2")
}
