//go:build dfcopychmodnonoctal

package dockerfile

import (
	"fmt"
	"testing"

	"github.com/containerd/continuity/fs/fstest"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/frontend/dockerui"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/stretchr/testify/require"
	"github.com/tonistiigi/fsutil"
)

func init() {
	allTests = append(allTests, integration.TestFuncs(testChmodNonOctal)...)
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

	expectedCommands := ""
	copyCommands := ""
	verifyCommands := ""

	for _, tc := range tcases {
		if tc.isDir {
			// create nested input dir because COPY copies directory contents
			expectedCommands += "RUN mkdir -p /input/dirs/" + tc.dst + " && cp -a /input/" + tc.src + " /input/dirs/" + tc.dst + "/" + tc.dst + "\n"
			expectedCommands += "RUN cp -a /input/dirs/" + tc.dst + "/. /expected/ && chmod " + tc.mode + " /expected/" + tc.dst + "\n"
			copyCommands += "COPY --from=base --chmod=" + tc.mode + " /input/dirs/" + tc.dst + " /\n"
		} else {
			expectedCommands += "RUN cp -a /input/" + tc.src + " /expected/" + tc.dst + " && chmod " + tc.mode + " /expected/" + tc.dst + "\n"
			copyCommands += "COPY --from=base --chmod=" + tc.mode + " /input/" + tc.src + " /" + tc.dst + "\n"
		}
		verifyCommands += "RUN [ \"$(stat -c %A /actual/" + tc.dst + ")\" = \"$(stat -c %A /expected/" + tc.dst + ")\" ]\n"
	}

	dockerfile := []byte(fmt.Sprintf(`
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

`, expectedCommands, copyCommands, verifyCommands))

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
