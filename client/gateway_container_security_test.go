package client

import (
	"bytes"
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/entitlements"
	"github.com/moby/buildkit/util/iohelper"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/moby/buildkit/util/testutil/workers"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
)

func testClientGatewayContainerInvalidSecurityMode(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureSecurityMode)
	requiresLinux(t)

	ctx := sb.Context()

	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()
	defer checkAllReleasable(t, c, sb, true)

	b := func(ctx context.Context, c client.Client) (*client.Result, error) {
		st := llb.Image("busybox:latest")

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
		})
		if err != nil {
			return nil, err
		}
		defer ctr.Release(ctx)

		stdout := bytes.NewBuffer(nil)
		stderr := bytes.NewBuffer(nil)

		pid, err := ctr.Start(ctx, client.StartRequest{
			Args:         []string{"sh", "-euc", `sec=$(awk '/^Seccomp:/{print $2}' /proc/self/status); echo "Seccomp=$sec"; test "$sec" = 0`},
			Stdout:       &iohelper.NopWriteCloser{Writer: stdout},
			Stderr:       &iohelper.NopWriteCloser{Writer: stderr},
			SecurityMode: pb.SecurityMode(2),
		})
		if err != nil {
			return nil, err
		}

		err = pid.Wait()

		t.Logf("Stdout: %q", stdout.String())
		t.Logf("Stderr: %q", stderr.String())

		return nil, err
	}

	_, err = c.Build(ctx, SolveOpt{}, "buildkit_test", b, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid security mode")
}

func testClientGatewayContainerSecurityMode(t *testing.T, sb integration.Sandbox, expectFail bool) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureSecurityMode)
	requiresLinux(t)

	ctx := sb.Context()

	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	product := "buildkit_test"

	command := []string{"sh", "-c", `cat /proc/self/status | grep CapEff | cut -f 2`}
	mode := llb.SecurityModeSandbox
	var allowedEntitlements []string
	var assertCaps func(caps uint64)
	secMode := sb.Value("secmode")
	if secMode == securitySandbox {
		assertCaps = func(caps uint64) {
			/*
				$ capsh --decode=00000000a80425fb
				0x00000000a80425fb=cap_chown,cap_dac_override,cap_fowner,cap_fsetid,cap_kill,cap_setgid,cap_setuid,cap_setpcap,
				cap_net_bind_service,cap_net_raw,cap_sys_chroot,cap_mknod,cap_audit_write,cap_setfcap
			*/
			require.Equal(t, uint64(0xa80425fb), caps)
		}
		allowedEntitlements = []string{}
		if expectFail {
			return
		}
	} else {
		assertCaps = func(caps uint64) {
			/*
				$ capsh --decode=0000003fffffffff
				0x0000003fffffffff=cap_chown,cap_dac_override,cap_dac_read_search,cap_fowner,cap_fsetid,cap_kill,cap_setgid,
				cap_setuid,cap_setpcap,cap_linux_immutable,cap_net_bind_service,cap_net_broadcast,cap_net_admin,cap_net_raw,
				cap_ipc_lock,cap_ipc_owner,cap_sys_module,cap_sys_rawio,cap_sys_chroot,cap_sys_ptrace,cap_sys_pacct,cap_sys_admin,
				cap_sys_boot,cap_sys_nice,cap_sys_resource,cap_sys_time,cap_sys_tty_config,cap_mknod,cap_lease,cap_audit_write,
				cap_audit_control,cap_setfcap,cap_mac_override,cap_mac_admin,cap_syslog,cap_wake_alarm,cap_block_suspend,cap_audit_read
			*/

			// require that _at least_ minimum capabilities are granted
			require.Equal(t, uint64(0x3fffffffff), caps&0x3fffffffff)
		}
		mode = llb.SecurityModeInsecure
		allowedEntitlements = []string{entitlements.EntitlementSecurityInsecure.String()}
		if expectFail {
			allowedEntitlements = []string{}
		}
	}

	b := func(ctx context.Context, c client.Client) (*client.Result, error) {
		st := llb.Image("busybox:latest")

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
		})
		if err != nil {
			return nil, err
		}

		stdout := bytes.NewBuffer(nil)
		stderr := bytes.NewBuffer(nil)

		pid, err := ctr.Start(ctx, client.StartRequest{
			Args:         command,
			Stdout:       &iohelper.NopWriteCloser{Writer: stdout},
			Stderr:       &iohelper.NopWriteCloser{Writer: stderr},
			SecurityMode: mode,
		})
		if err != nil {
			ctr.Release(ctx)
			return nil, err
		}
		defer ctr.Release(ctx)

		err = pid.Wait()

		t.Logf("Stdout: %q", stdout.String())
		t.Logf("Stderr: %q", stderr.String())

		if expectFail {
			require.Error(t, err)
			require.Contains(t, err.Error(), "security.insecure is not allowed")
			return nil, err
		}

		require.NoError(t, err)

		capsValue, err := strconv.ParseUint(strings.TrimSpace(stdout.String()), 16, 64)
		require.NoError(t, err)

		assertCaps(capsValue)

		return &client.Result{}, nil
	}

	solveOpts := SolveOpt{
		AllowedEntitlements: allowedEntitlements,
	}
	_, err = c.Build(ctx, solveOpts, product, b, nil)

	if expectFail {
		require.Error(t, err)
		require.Contains(t, err.Error(), "security.insecure is not allowed")
	} else {
		require.NoError(t, err)
	}

	checkAllReleasable(t, c, sb, true)
}

// testClientGatewayContainerSecurityModeCaps ensures that the correct security mode
// is propagated to the gateway container
func testClientGatewayContainerSecurityModeCaps(t *testing.T, sb integration.Sandbox) {
	testClientGatewayContainerSecurityMode(t, sb, false)
}

func testClientGatewayContainerSecurityModeValidation(t *testing.T, sb integration.Sandbox) {
	testClientGatewayContainerSecurityMode(t, sb, true)
}
