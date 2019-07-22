// +build dfrunsecurity

package dockerfile

import (
	"context"
	"os"
	"testing"

	"github.com/containerd/continuity/fs/fstest"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/frontend/dockerfile/builder"
	"github.com/moby/buildkit/util/entitlements"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/stretchr/testify/require"
)

var runSecurityTests = []integration.Test{
	testRunSecurityInsecure,
	testRunSecuritySandbox,
	testRunSecurityDefault,
}

func init() {
	securityTests = append(securityTests, runSecurityTests...)

}

func testRunSecurityInsecure(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM busybox
RUN --security=insecure [ "$(cat /proc/self/status | grep CapBnd)" == "CapBnd:	0000003fffffffff" ]
`)

	dir, err := tmpdir(
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
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
		AllowedEntitlements: []entitlements.Entitlement{entitlements.EntitlementSecurityInsecure},
	}, nil)

	secMode := sb.Value("secmode")
	switch secMode {
	case securitySandbox:
		require.Error(t, err)
		require.Contains(t, err.Error(), "entitlement security.insecure is not allowed")
	case securityInsecure:
		require.NoError(t, err)
	default:
		require.Fail(t, "unexpected secmode")
	}
}

func testRunSecuritySandbox(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM busybox
RUN --security=sandbox [ "$(cat /proc/self/status | grep CapBnd)" == "CapBnd:	00000000a80425fb" ]
`)

	dir, err := tmpdir(
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
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

	require.NoError(t, err)
}

func testRunSecurityDefault(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM busybox
RUN [ "$(cat /proc/self/status | grep CapBnd)" == "CapBnd:	00000000a80425fb" ]
`)

	dir, err := tmpdir(
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
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
		AllowedEntitlements: []entitlements.Entitlement{entitlements.EntitlementSecurityInsecure},
	}, nil)

	secMode := sb.Value("secmode")
	switch secMode {
	case securitySandbox:
		require.Error(t, err)
		require.Contains(t, err.Error(), "entitlement security.insecure is not allowed")
	case securityInsecure:
		require.NoError(t, err)
	default:
		require.Fail(t, "unexpected secmode")
	}
}
