package dockerfile

import (
	"os"
	"testing"

	"github.com/containerd/continuity/fs/fstest"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/frontend/dockerfile/builder"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/secrets/secretsprovider"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/stretchr/testify/require"
)

var secretsTests = []integration.Test{
	testSecretFileParams,
	testSecretRequiredWithoutValue,
}

func init() {
	allTests = append(allTests, secretsTests...)
}

func testSecretFileParams(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM busybox
RUN --mount=type=secret,required=false,mode=741,uid=100,gid=102,target=/mysecret [ "$(stat -c "%u %g %f" /mysecret)" = "100 102 81e1" ]
RUN [ ! -f /mysecret ] # check no stub left behind
`)

	dir, err := tmpdir(
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)
	require.NoError(t, err)
	defer os.RemoveAll(dir)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		LocalDirs: map[string]string{
			builder.DefaultLocalNameDockerfile: dir,
			builder.DefaultLocalNameContext:    dir,
		},
		Session: []session.Attachable{secretsprovider.FromMap(map[string][]byte{
			"mysecret": []byte("pw"),
		})},
	}, nil)
	require.NoError(t, err)
}

func testSecretRequiredWithoutValue(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM busybox
RUN --mount=type=secret,required,id=mysecret foo
`)

	dir, err := tmpdir(
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)
	require.NoError(t, err)
	defer os.RemoveAll(dir)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		LocalDirs: map[string]string{
			builder.DefaultLocalNameDockerfile: dir,
			builder.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "secret mysecret: not found")
}
