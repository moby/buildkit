package client

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/entitlements"
	"github.com/moby/buildkit/util/iohelper"
	"github.com/moby/buildkit/util/testutil/echoserver"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
)

func testClientGatewayContainerExtraHosts(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)

	ctx := sb.Context()
	product := "buildkit_test"

	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	b := func(ctx context.Context, c client.Client) (*client.Result, error) {
		st := llb.Image("busybox")

		def, err := st.Marshal(ctx)
		if err != nil {
			return nil, errors.Wrap(err, "failed to marshal state")
		}

		r, err := c.Solve(ctx, client.SolveRequest{
			Definition: def.ToPB(),
		})
		if err != nil {
			return nil, errors.Wrap(err, "failed to solve")
		}

		ctr, err := c.NewContainer(ctx, client.NewContainerRequest{
			Mounts: []client.Mount{{
				Dest:      "/",
				MountType: pb.MountType_BIND,
				Ref:       r.Ref,
			}},
			ExtraHosts: []*pb.HostIP{{
				Host: "some.host",
				IP:   "169.254.11.22",
			}},
		})
		if err != nil {
			return nil, err
		}

		stdout := bytes.NewBuffer(nil)
		stderr := bytes.NewBuffer(nil)

		pid, err := ctr.Start(ctx, client.StartRequest{
			Args:   []string{"grep", "169.254.11.22\tsome.host", "/etc/hosts"},
			Stdout: &iohelper.NopWriteCloser{Writer: stdout},
			Stderr: &iohelper.NopWriteCloser{Writer: stderr},
		})
		if err != nil {
			ctr.Release(ctx)
			return nil, err
		}
		defer ctr.Release(ctx)

		err = pid.Wait()

		t.Logf("Stdout: %q", stdout.String())
		t.Logf("Stderr: %q", stderr.String())

		require.NoError(t, err)

		return &client.Result{}, nil
	}

	_, err = c.Build(ctx, SolveOpt{}, product, b, nil)
	require.NoError(t, err)

	checkAllReleasable(t, c, sb, true)
}

func testClientGatewayContainerHostNetworking(t *testing.T, sb integration.Sandbox, expectFail bool) {
	if os.Getenv("BUILDKIT_RUN_NETWORK_INTEGRATION_TESTS") == "" {
		t.SkipNow()
	}

	if sb.Rootless() && sb.Value("netmode") == defaultNetwork {
		// skip "default" network test for rootless, it always runs with "host" network
		// https://github.com/moby/buildkit/blob/v0.9.0/docs/rootless.md#known-limitations
		t.SkipNow()
	}

	requiresLinux(t)

	ctx := sb.Context()
	product := "buildkit_test"

	var allowedEntitlements []string
	netMode := pb.NetMode_UNSET
	if sb.Value("netmode") == hostNetwork {
		netMode = pb.NetMode_HOST
		allowedEntitlements = []string{entitlements.EntitlementNetworkHost.String()}
		if expectFail {
			allowedEntitlements = []string{}
		}
	}
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	s, err := echoserver.NewTestServer("foo")
	require.NoError(t, err)
	addrParts := strings.Split(s.Addr().String(), ":")
	port := addrParts[len(addrParts)-1]

	b := func(ctx context.Context, c client.Client) (*client.Result, error) {
		st := llb.Image("busybox")

		def, err := st.Marshal(ctx)
		if err != nil {
			return nil, errors.Wrap(err, "failed to marshal state")
		}

		r, err := c.Solve(ctx, client.SolveRequest{
			Definition: def.ToPB(),
		})
		if err != nil {
			return nil, errors.Wrap(err, "failed to solve")
		}

		ctr, err := c.NewContainer(ctx, client.NewContainerRequest{
			Mounts: []client.Mount{{
				Dest:      "/",
				MountType: pb.MountType_BIND,
				Ref:       r.Ref,
			}},
			NetMode: netMode,
		})
		if err != nil {
			return nil, err
		}

		stdout := bytes.NewBuffer(nil)
		stderr := bytes.NewBuffer(nil)

		pid, err := ctr.Start(ctx, client.StartRequest{
			Args:   []string{"/bin/sh", "-c", fmt.Sprintf("nc 127.0.0.1 %s | grep foo", port)},
			Stdout: &iohelper.NopWriteCloser{Writer: stdout},
			Stderr: &iohelper.NopWriteCloser{Writer: stderr},
		})
		if err != nil {
			ctr.Release(ctx)
			return nil, err
		}
		defer ctr.Release(ctx)

		err = pid.Wait()

		t.Logf("Stdout: %q", stdout.String())
		t.Logf("Stderr: %q", stderr.String())

		if netMode == pb.NetMode_HOST {
			if expectFail {
				require.Error(t, err)
				require.Contains(t, err.Error(), "network.host is not allowed")
			} else {
				require.NoError(t, err)
			}
		} else {
			require.Error(t, err)
		}

		return &client.Result{}, nil
	}

	solveOpts := SolveOpt{
		AllowedEntitlements: allowedEntitlements,
	}
	_, err = c.Build(ctx, solveOpts, product, b, nil)
	require.NoError(t, err)

	checkAllReleasable(t, c, sb, true)
}

func testClientGatewayContainerHostNetworkingAccess(t *testing.T, sb integration.Sandbox) {
	testClientGatewayContainerHostNetworking(t, sb, false)
}

func testClientGatewayContainerHostNetworkingValidation(t *testing.T, sb integration.Sandbox) {
	testClientGatewayContainerHostNetworking(t, sb, true)
}
