package dockerfile

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"

	"github.com/containerd/continuity/fs/fstest"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/frontend/dockerfile/builder"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/sshforward/sshprovider"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/stretchr/testify/require"
)

var sshTests = []integration.Test{
	testSSHSocketParams,
}

func init() {
	allTests = append(allTests, sshTests...)
}

func testSSHSocketParams(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM busybox
RUN --mount=type=ssh,mode=741,uid=100,gid=102 [ "$(stat -c "%u %g %f" $SSH_AUTH_SOCK)" = "100 102 c1e1" ]
`)

	dir, err := tmpdir(
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)
	require.NoError(t, err)
	defer os.RemoveAll(dir)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	k, err := rsa.GenerateKey(rand.Reader, 1024)
	require.NoError(t, err)

	dt := pem.EncodeToMemory(
		&pem.Block{
			Type:  "RSA PRIVATE KEY",
			Bytes: x509.MarshalPKCS1PrivateKey(k),
		},
	)

	tmpDir, err := ioutil.TempDir("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	err = ioutil.WriteFile(filepath.Join(tmpDir, "key"), dt, 0600)
	require.NoError(t, err)

	ssh, err := sshprovider.NewSSHAgentProvider([]sshprovider.AgentConfig{{
		Paths: []string{filepath.Join(tmpDir, "key")},
	}})
	require.NoError(t, err)

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		LocalDirs: map[string]string{
			builder.DefaultLocalNameDockerfile: dir,
			builder.DefaultLocalNameContext:    dir,
		},
		Session: []session.Attachable{ssh},
	}, nil)
	require.NoError(t, err)
}
