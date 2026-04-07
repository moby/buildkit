package dockerfile

import (
	"strings"
	"testing"
	"time"

	"github.com/containerd/continuity/fs/fstest"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/frontend/dockerui"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/secrets/secretsprovider"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tonistiigi/fsutil"
)

var secretsTests = integration.TestFuncs(
	testSecretFileParams,
	testSecretRequiredWithoutValue,
	testSecretAsEnviron,
	testSecretAsEnvironWithFileMount,
)

func init() {
	allTests = append(allTests, secretsTests...)
}

// testSecretFileParams verifies that a secret mounted with custom permissions
// (mode, uid, gid) has the correct ownership and mode, and that no stub file
// is left behind after the RUN step.
func testSecretFileParams(t *testing.T, sb integration.Sandbox) {
	// mode/uid/gid are POSIX-only; BuildKit has no code path to map them to Windows ACLs.
	integration.SkipOnPlatform(t, "windows", "Secret permission parameters not implemented for Windows in BuildKit")
	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM busybox
RUN --mount=type=secret,required=false,mode=741,uid=100,gid=102,target=/mysecret [ "$(stat -c "%u %g %f" /mysecret)" = "100 102 81e1" ]
RUN [ ! -f /mysecret ] # check no stub left behind
`)

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
		Session: []session.Attachable{secretsprovider.FromMap(map[string][]byte{
			"mysecret": []byte("pw"),
		})},
	}, nil)
	require.NoError(t, err)
}

// testSecretRequiredWithoutValue verifies that a build fails when a required
// secret is referenced in the Dockerfile but no value is supplied by the client.
func testSecretRequiredWithoutValue(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(integration.UnixOrWindows(
		`FROM busybox 
		RUN --mount=type=secret,required,id=mysecret foo`,

		`FROM nanoserver
		USER ContainerAdministrator
		RUN --mount=type=secret,required,id=mysecret foo`,
	))

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
	require.Error(t, err)
	require.Contains(t, err.Error(), "secret mysecret: not found")
}

// testSecretAsEnviron verifies that a secret injected via env= is accessible
// as an environment variable, is not left behind as a file mount, and is not
// leaked in the vertex display name.
func testSecretAsEnviron(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	// Forward slashes in Windows paths because the Dockerfile parser
	// consumes backslashes as escapes.
	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM busybox
ENV SECRET_ENV=foo
RUN --mount=type=secret,id=mysecret,env=SECRET_ENV [ "$SECRET_ENV" == "pw" ] && [ ! -f /run/secrets/mysecret ] || false
`,
		`
FROM nanoserver
USER ContainerAdministrator
ENV SECRET_ENV=foo
RUN --mount=type=secret,id=mysecret,env=SECRET_ENV if %SECRET_ENV% NEQ pw (exit 1) & if exist C:/run/secrets/mysecret (exit 1)
`,
	))

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	done := make(chan struct{})
	status := make(chan *client.SolveStatus)
	hasStatus := false

	// Match the secret-path substring in the vertex name (differs per platform).
	vertexSearch := integration.UnixOrWindows("/run/secrets/mysecret", "C:/run/secrets/mysecret")
	// Linux: $SECRET_ENV scrubbed to "****".  Windows: %VAR% not expanded by lexer, stays literal.
	maskedExpect := integration.UnixOrWindows(`[ "****" == "pw" ] && `, `%SECRET_ENV% NEQ pw`)

	go func() {
		for st := range status {
			for _, v := range st.Vertexes {
				if strings.Contains(v.Name, vertexSearch) {
					hasStatus = true
					assert.Contains(t, v.Name, maskedExpect)
				}
			}
		}
		close(done)
	}()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
		Session: []session.Attachable{secretsprovider.FromMap(map[string][]byte{
			"mysecret": []byte("pw"),
		})},
	}, status)
	require.NoError(t, err)

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		require.Fail(t, "timed out waiting for status")
	}

	require.True(t, hasStatus)
}

// testSecretAsEnvironWithFileMount verifies that a secret with both env= and
// target= is accessible as an environment variable and as a file.
func testSecretAsEnvironWithFileMount(t *testing.T, sb integration.Sandbox) {
	// target= triggers a tmpfs-backed file mount; Windows only accepts "windows-layer" mounts.
	integration.SkipOnPlatform(t, "windows", "secret file mounts use tmpfs which is unsupported on Windows")
	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM busybox
RUN --mount=type=secret,id=mysecret,target=/run/secrets/secret,env=SECRET_ENV [ "$SECRET_ENV" == "pw" ] && [ -f /run/secrets/secret ] || false
`)

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
		Session: []session.Attachable{secretsprovider.FromMap(map[string][]byte{
			"mysecret": []byte("pw"),
		})},
	}, nil)
	require.NoError(t, err)
}
