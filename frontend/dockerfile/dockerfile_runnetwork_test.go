package dockerfile

import (
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

var runNetworkTests = integration.TestFuncs(
	testRunDefaultNetwork,
	testRunNoNetwork,
	testRunHostNetwork,
	testRunGlobalNetwork,
)

func init() {
	networkTests = append(networkTests, runNetworkTests...)
}

func testRunDefaultNetwork(t *testing.T, sb integration.Sandbox) {
	if os.Getenv("BUILDKIT_RUN_NETWORK_INTEGRATION_TESTS") == "" {
		t.SkipNow()
	}
	if sb.Rootless() {
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

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		LocalDirs: map[string]string{
			builder.DefaultLocalNameDockerfile: dir,
			builder.DefaultLocalNameContext:    dir,
		},
	}, nil)

	require.NoError(t, err)
}

func testRunNoNetwork(t *testing.T, sb integration.Sandbox) {
	if os.Getenv("BUILDKIT_RUN_NETWORK_INTEGRATION_TESTS") == "" {
		t.SkipNow()
	}

	f := getFrontend(t, sb)

	dockerfile := `
FROM busybox
RUN --network=none ! ip link show eth0
`

	if !sb.Rootless() {
		dockerfile += "RUN ip link show eth0"
	}

	dir, err := tmpdir(
		fstest.CreateFile("Dockerfile", []byte(dockerfile), 0600),
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

	require.NoError(t, err)
}

func testRunHostNetwork(t *testing.T, sb integration.Sandbox) {
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
`, port)

	if !sb.Rootless() {
		dockerfile += fmt.Sprintf(`RUN ! nc 127.0.0.1 %s | grep foo`, port)
	}

	dir, err := tmpdir(
		fstest.CreateFile("Dockerfile", []byte(dockerfile), 0600),
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

func testRunGlobalNetwork(t *testing.T, sb integration.Sandbox) {
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

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
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
