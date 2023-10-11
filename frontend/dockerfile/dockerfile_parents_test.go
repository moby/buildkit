//go:build dfparents
// +build dfparents

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
)

var parentsTests = integration.TestFuncs(
	testCopyParents,
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
		LocalDirs: map[string]string{
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
