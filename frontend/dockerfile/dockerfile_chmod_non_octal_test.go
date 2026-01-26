package dockerfile

import (
	"fmt"
	"strings"
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

	var expectedCommands strings.Builder
	var copyCommands strings.Builder
	var verifyCommands strings.Builder

	for _, tc := range tcases {
		if tc.isDir {
			// create nested input dir because COPY copies directory contents
			expectedCommands.WriteString("RUN mkdir -p /input/dirs/")
			expectedCommands.WriteString(tc.dst)
			expectedCommands.WriteString(" && cp -a /input/")
			expectedCommands.WriteString(tc.src)
			expectedCommands.WriteString(" /input/dirs/")
			expectedCommands.WriteString(tc.dst)
			expectedCommands.WriteByte('/')
			expectedCommands.WriteString(tc.dst)
			expectedCommands.WriteByte('\n')
			expectedCommands.WriteString("RUN cp -a /input/dirs/")
			expectedCommands.WriteString(tc.dst)
			expectedCommands.WriteString("/. /expected/ && chmod ")
			expectedCommands.WriteString(tc.mode)
			expectedCommands.WriteString(" /expected/")
			expectedCommands.WriteString(tc.dst)
			expectedCommands.WriteByte('\n')
			copyCommands.WriteString("COPY --from=base --chmod=")
			copyCommands.WriteString(tc.mode)
			copyCommands.WriteString(" /input/dirs/")
			copyCommands.WriteString(tc.dst)
			copyCommands.WriteString(" /\n")
		} else {
			expectedCommands.WriteString("RUN cp -a /input/")
			expectedCommands.WriteString(tc.src)
			expectedCommands.WriteString(" /expected/")
			expectedCommands.WriteString(tc.dst)
			expectedCommands.WriteString(" && chmod ")
			expectedCommands.WriteString(tc.mode)
			expectedCommands.WriteString(" /expected/")
			expectedCommands.WriteString(tc.dst)
			expectedCommands.WriteByte('\n')
			copyCommands.WriteString("COPY --from=base --chmod=")
			copyCommands.WriteString(tc.mode)
			copyCommands.WriteString(" /input/")
			copyCommands.WriteString(tc.src)
			copyCommands.WriteString(" /")
			copyCommands.WriteString(tc.dst)
			copyCommands.WriteByte('\n')
		}
		verifyCommands.WriteString("RUN [ \"$(stat -c %A /actual/")
		verifyCommands.WriteString(tc.dst)
		verifyCommands.WriteString(")\" = \"$(stat -c %A /expected/")
		verifyCommands.WriteString(tc.dst)
		verifyCommands.WriteString(")\" ]\n")
	}

	dockerfile := fmt.Appendf(nil, `
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

`, expectedCommands.String(), copyCommands.String(), verifyCommands.String())

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
