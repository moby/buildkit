package dockerfile

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/containerd/continuity/fs/fstest"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/frontend/dockerfile/builder"
	"github.com/moby/buildkit/solver/errdefs"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/stretchr/testify/require"
)

func testErrorsSourceMap(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	tcases := []struct {
		name       string
		dockerfile string
		errorLine  []int
	}{
		{
			name: "invalidenv",
			dockerfile: `from alpine
env`,
			errorLine: []int{2},
		},
		{
			name: "invalidsyntax",
			dockerfile: `#syntax=foobar
from alpine`,
			errorLine: []int{1},
		},
		{
			name: "invalidrun",
			dockerfile: `from scratch
env foo=bar
run what`,
			errorLine: []int{3},
		},
		{
			name: "invalidcopy",
			dockerfile: `from scratch
env foo=bar
copy foo bar
env bar=baz`,
			errorLine: []int{3},
		},
		{
			name: "invalidflag",
			dockerfile: `from scratch
env foo=bar
copy --foo=bar / /
env bar=baz`,
			errorLine: []int{3},
		},
		{
			name: "invalidcopyfrom",
			dockerfile: `from scratch
env foo=bar
copy --from=invalid foo bar
env bar=baz`,
			errorLine: []int{3},
		},
		{
			name: "invalidmultiline",
			dockerfile: `from scratch
run wh\
at
env bar=baz`,
			errorLine: []int{2, 3},
		},
	}

	for _, tc := range tcases {
		t.Run(tc.name, func(t *testing.T) {
			dir, err := tmpdir(
				fstest.CreateFile("Dockerfile", []byte(tc.dockerfile), 0600),
			)
			require.NoError(t, err)
			defer os.RemoveAll(dir)

			c, err := client.New(context.TODO(), sb.Address())
			require.NoError(t, err)
			defer c.Close()

			_, err = f.Solve(context.TODO(), c, client.SolveOpt{
				LocalDirs: map[string]string{
					builder.DefaultLocalNameDockerfile: dir,
					builder.DefaultLocalNameContext:    dir,
				},
			}, nil)
			require.Error(t, err)

			srcs := errdefs.Sources(err)
			require.Equal(t, 1, len(srcs))

			require.Equal(t, "Dockerfile", srcs[0].Info.Filename)
			require.Equal(t, tc.dockerfile, string(srcs[0].Info.Data))
			require.Equal(t, len(tc.errorLine), len(srcs[0].Ranges))
			require.NotNil(t, srcs[0].Info.Definition)

		next:
			for _, l := range tc.errorLine {
				for _, l2 := range srcs[0].Ranges {
					if l2.Start.Line == int32(l) {
						continue next
					}
				}
				require.Fail(t, fmt.Sprintf("line %d not found", l))
			}
		})
	}
}
