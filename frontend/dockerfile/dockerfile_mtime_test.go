//go:build dfmtime

package dockerfile

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/containerd/continuity/fs/fstest"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/frontend/dockerui"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/moby/buildkit/util/testutil/workers"
	"github.com/stretchr/testify/require"
	"github.com/tonistiigi/fsutil"
)

func init() {
	allTests = append(allTests, integration.TestFuncs(
		testCopyMtime,
	)...)
}

func testCopyMtime(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	workers.CheckFeatureCompat(t, sb, workers.FeatureOCIExporter)
	f := getFrontend(t, sb)

	srcTime := time.Unix(1700000000, 0)
	dockerfile := []byte(`
ARG SOURCE_DATE_EPOCH=42
FROM scratch AS srcs
ARG SOURCE_DATE_EPOCH
COPY --mtime=$SOURCE_DATE_EPOCH sub/hello.txt /a/b/hello.txt
COPY --mtime=@42 extra.txt /c/extra.txt
FROM scratch
COPY --from=srcs / /
`)
	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateDir("sub", 0755),
		fstest.CreateFile("sub/hello.txt", []byte("hello"), 0644),
		fstest.CreateFile("extra.txt", []byte("extra"), 0644),
		fstest.Chtimes("sub/hello.txt", srcTime, srcTime),
		fstest.Chtimes("extra.txt", srcTime, srcTime),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	out := filepath.Join(t.TempDir(), "out.tar")
	outW, err := os.Create(out)
	require.NoError(t, err)
	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		Exports: []client.ExportEntry{{
			Type:   client.ExporterOCI,
			Output: fixedWriteCloser(outW),
		}},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(out)
	require.NoError(t, err)
	mfst := readOCIManifest(t, dt)
	require.Len(t, mfst.Layers, 1)
	layer := readOCILayerMap(t, dt, mfst.Layers[0])
	for _, p := range []string{"a/", "a/b/", "a/b/hello.txt", "c/", "c/extra.txt"} {
		item, ok := layer[p]
		require.True(t, ok, "%s not found in %v", p, layer)
		require.Equal(t, int64(42), item.Header.ModTime.Unix(), "mtime of %s", p)
	}
}
