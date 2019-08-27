// +build dfrunnetwork

package dockerfile

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/containerd/continuity/fs/fstest"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/frontend/dockerfile/builder"
	"github.com/moby/buildkit/util/entitlements"
	"github.com/moby/buildkit/util/testutil/echoserver"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/stretchr/testify/require"
)

var runNetworkTests = []integration.Test{
	runDefaultNetwork,
	runNoNetwork,
	runHostNetwork,
	runGlobalNetwork,
}

func init() {
	networkTests = append(networkTests, runNetworkTests...)
}

func runDefaultNetwork(t *testing.T, sb integration.Sandbox) {
	if os.Getenv("BUILDKIT_RUN_NETWORK_INTEGRATION_TESTS") == "" {
		t.SkipNow()
	}

	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM busybox
RUN ip link show eth0
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

func runNoNetwork(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM busybox
RUN --network=none ! ip link show eth0
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

func runHostNetwork(t *testing.T, sb integration.Sandbox) {
	if os.Getenv("BUILDKIT_RUN_NETWORK_INTEGRATION_TESTS") == "" {
		t.SkipNow()
	}

	f := getFrontend(t, sb)

	s, err := echoserver.NewTestServer("foo")
	require.NoError(t, err)
	addrParts := strings.Split(s.Addr().String(), ":")
	port := addrParts[len(addrParts)-1]

	dockerfile := fmt.Sprintf(`
FROM busybox
RUN --network=host nc 127.0.0.1 %s | grep foo
RUN ! nc 127.0.0.1 %s | grep foo
`, port, port)

	dir, err := tmpdir(
		fstest.CreateFile("Dockerfile", []byte(dockerfile), 0600),
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
		AllowedEntitlements: []entitlements.Entitlement{entitlements.EntitlementNetworkHost},
	}, nil)

	hostAllowed := sb.Value("network.host")
	switch hostAllowed {
	case networkHostGranted:
		require.NoError(t, err)
	case networkHostDenied:
		require.Error(t, err)
		require.Contains(t, err.Error(), "entitlement network.host is not allowed")
	default:
		require.Fail(t, "unexpected network.host mode %q", hostAllowed)
	}
}

func runGlobalNetwork(t *testing.T, sb integration.Sandbox) {
	if os.Getenv("BUILDKIT_RUN_NETWORK_INTEGRATION_TESTS") == "" {
		t.SkipNow()
	}

	f := getFrontend(t, sb)

	s, err := echoserver.NewTestServer("foo")
	require.NoError(t, err)
	addrParts := strings.Split(s.Addr().String(), ":")
	port := addrParts[len(addrParts)-1]

	dockerfile := fmt.Sprintf(`
FROM busybox
RUN nc 127.0.0.1 %s | grep foo
RUN --network=none ! nc -z 127.0.0.1 %s
`, port, port)

	dir, err := tmpdir(
		fstest.CreateFile("Dockerfile", []byte(dockerfile), 0600),
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
		AllowedEntitlements: []entitlements.Entitlement{entitlements.EntitlementNetworkHost},
		FrontendAttrs: map[string]string{
			"force-network-mode": "host",
		},
	}, nil)

	hostAllowed := sb.Value("network.host")
	switch hostAllowed {
	case networkHostGranted:
		require.NoError(t, err)
	case networkHostDenied:
		require.Error(t, err)
		require.Contains(t, err.Error(), "entitlement network.host is not allowed")
	default:
		require.Fail(t, "unexpected network.host mode %q", hostAllowed)
	}
}
