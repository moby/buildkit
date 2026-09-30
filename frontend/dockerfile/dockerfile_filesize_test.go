package dockerfile

import (
	"bytes"
	"testing"

	"github.com/containerd/continuity/fs/fstest"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/frontend/dockerui"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/stretchr/testify/require"
	"github.com/tonistiigi/fsutil"
)

func testDockerfileTooLarge(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	// pad past the read limit with comment lines that stay under the
	// parser's per-line limit
	line := append([]byte("# "), bytes.Repeat([]byte("x"), 4096)...)
	line = append(line, '\n')
	dockerfile := []byte(integration.UnixOrWindows("FROM scratch\n", "FROM nanoserver\n"))
	dockerfile = append(dockerfile, bytes.Repeat(line, (17<<20)/len(line))...)

	dir := integration.Tmpdir(t,
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
	require.ErrorContains(t, err, "Dockerfile exceeds maximum allowed size")
}
