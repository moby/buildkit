package client

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/frontend/gateway/client"
	gatewayapi "github.com/moby/buildkit/frontend/gateway/pb"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/solver/errdefs"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/iohelper"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
)

// testClientGatewayContainerCancelExecTty is testing the tty shuts down cleanly
// on context.Cancel
func testClientGatewayContainerCancelExecTty(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)
	ctx := sb.Context()

	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	product := "buildkit_test"

	inputR, inputW := io.Pipe()
	output := bytes.NewBuffer(nil)
	b := func(ctx context.Context, c client.Client) (*client.Result, error) {
		ctx, timeout := context.WithTimeoutCause(ctx, 10*time.Second, nil)
		defer timeout()
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
		require.NoError(t, err)

		pid1, err := ctr.Start(ctx, client.StartRequest{
			Args: []string{"sleep", "10"},
		})
		require.NoError(t, err)

		defer pid1.Wait()
		defer ctr.Release(ctx)

		execCtx, cancel := context.WithCancelCause(ctx)
		defer func() { cancel(errors.WithStack(context.Canceled)) }()

		prompt := newTestPrompt(execCtx, t, inputW, output)
		pid2, err := ctr.Start(execCtx, client.StartRequest{
			Args:   []string{"sh"},
			Tty:    true,
			Stdin:  inputR,
			Stdout: &iohelper.NopWriteCloser{Writer: output},
			Stderr: &iohelper.NopWriteCloser{Writer: output},
			Env:    []string{fmt.Sprintf("PS1=%s", prompt.String())},
		})
		require.NoError(t, err)

		prompt.SendExpect("echo hi", "hi")
		cancel(errors.WithStack(context.Canceled))

		err = pid2.Wait()
		require.ErrorIs(t, err, context.Canceled)

		return &client.Result{}, err
	}

	_, err = c.Build(ctx, SolveOpt{}, product, b, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), context.Canceled.Error())

	inputW.Close()
	inputR.Close()

	checkAllReleasable(t, c, sb, true)
}

// testClientGatewayContainerCancelOnRelease is testing that all running
// processes are terminated when the container is released.
func testClientGatewayContainerCancelOnRelease(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)

	ctx := sb.Context()

	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	product := "buildkit_test"

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

		start := time.Now()
		defer func() {
			// ensure pid1 and pid2 exit from cancel before the 10s sleep
			// exits naturally
			require.WithinDuration(t, start, time.Now(), 10*time.Second)
		}()

		// background pid1 process that starts container
		pid1, err := ctr.Start(ctx, client.StartRequest{
			Args: []string{"sleep", "10"},
		})
		require.NoError(t, err)

		pid2, err := ctr.Start(ctx, client.StartRequest{
			Args: []string{"sleep", "10"},
		})
		require.NoError(t, err)

		ctr.Release(ctx)
		err = pid1.Wait()
		require.Contains(t, err.Error(), context.Canceled.Error())

		err = pid2.Wait()
		require.Contains(t, err.Error(), context.Canceled.Error())

		return &client.Result{}, nil
	}

	_, err = c.Build(ctx, SolveOpt{}, product, b, nil)
	require.NoError(t, err)
	checkAllReleasable(t, c, sb, true)
}

// testClientGatewayContainerCancelPID1Tty is testing that the tty will cleanly
// shutdown on context cancel
func testClientGatewayContainerCancelPID1Tty(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)
	ctx := sb.Context()

	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	product := "buildkit_test"

	inputR, inputW := io.Pipe()
	output := bytes.NewBuffer(nil)

	b := func(ctx context.Context, c client.Client) (*client.Result, error) {
		ctx, cancel := context.WithTimeoutCause(ctx, 10*time.Second, nil)
		defer cancel()

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
		require.NoError(t, err)
		defer ctr.Release(ctx)

		prompt := newTestPrompt(ctx, t, inputW, output)
		pid1, err := ctr.Start(ctx, client.StartRequest{
			Args:   []string{"sh"},
			Tty:    true,
			Stdin:  inputR,
			Stdout: &iohelper.NopWriteCloser{Writer: output},
			Stderr: &iohelper.NopWriteCloser{Writer: output},
			Env:    []string{fmt.Sprintf("PS1=%s", prompt.String())},
		})
		require.NoError(t, err)
		prompt.SendExpect("echo hi", "hi")
		cancel()

		err = pid1.Wait()
		require.ErrorIs(t, err, context.Canceled)

		return &client.Result{}, err
	}

	_, err = c.Build(ctx, SolveOpt{}, product, b, nil)
	require.Error(t, err)

	inputW.Close()
	inputR.Close()

	checkAllReleasable(t, c, sb, true)
}

// testClientGatewayContainerExecPipe is testing the ability to pipe multiple
// process together all started via `Exec` into the same container.
// We are mimicing: `echo testing | cat | cat > /tmp/foo && cat /tmp/foo`
func testClientGatewayContainerExecPipe(t *testing.T, sb integration.Sandbox) {
	testClientGatewayContainerExecPipeWithCleanup(t, sb, func(ctx context.Context, ctr client.Container, pid1 client.ContainerProcess, stdin *io.PipeWriter) {
		stdin.Close()
		pid1.Wait()
		ctr.Release(context.WithoutCancel(ctx))
	})
}

// testClientGatewayContainerExecPipeRelease verifies that releasing the
// container while pid1 still has an open stdin reader does not leave runc
// blocked on the stdin copy.
func testClientGatewayContainerExecPipeRelease(t *testing.T, sb integration.Sandbox) {
	testClientGatewayContainerExecPipeWithCleanup(t, sb, func(ctx context.Context, ctr client.Container, pid1 client.ContainerProcess, _ *io.PipeWriter) {
		ctr.Release(context.WithoutCancel(ctx))
		pid1.Wait()
	})
}

// testClientGatewayContainerExecPipeSignalKill verifies that SIGKILL delivered
// via the process Signal channel unblocks runc when pid1 still has an open
// stdin reader.
func testClientGatewayContainerExecPipeSignalKill(t *testing.T, sb integration.Sandbox) {
	testClientGatewayContainerExecPipeWithCleanup(t, sb, func(ctx context.Context, ctr client.Container, pid1 client.ContainerProcess, _ *io.PipeWriter) {
		pid1.Signal(ctx, syscall.SIGKILL)
		pid1.Wait()
		ctr.Release(context.WithoutCancel(ctx))
	})
}

func testClientGatewayContainerExecPipeWithCleanup(t *testing.T, sb integration.Sandbox, cleanup func(ctx context.Context, ctr client.Container, pid1 client.ContainerProcess, stdin *io.PipeWriter)) {
	requiresLinux(t)

	ctx := sb.Context()

	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	product := "buildkit_test"

	output := bytes.NewBuffer(nil)

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

		// background pid1 process that starts container
		pid1StdinR, pid1StdinW := io.Pipe()
		pid1, err := ctr.Start(ctx, client.StartRequest{
			Args:  []string{"cat"},
			Stdin: pid1StdinR,
		})
		if err != nil {
			ctr.Release(context.WithoutCancel(ctx))
			return nil, err
		}

		defer cleanup(ctx, ctr, pid1, pid1StdinW)

		// first part is `echo testing | cat`
		stdin2 := bytes.NewBuffer([]byte("testing"))
		stdin3, stdout2 := io.Pipe()

		pid2, err := ctr.Start(ctx, client.StartRequest{
			Args:   []string{"cat"},
			Cwd:    "/",
			Tty:    false,
			Stdin:  io.NopCloser(stdin2),
			Stdout: stdout2,
		})
		if err != nil {
			return nil, err
		}

		// next part is: `| cat > /tmp/test`
		pid3, err := ctr.Start(ctx, client.StartRequest{
			Args:  []string{"sh", "-c", "cat > /tmp/test"},
			Stdin: stdin3,
		})
		if err != nil {
			return nil, err
		}

		err = pid2.Wait()
		if err != nil {
			stdout2.Close()
			return nil, err
		}

		err = stdout2.Close()
		if err != nil {
			return nil, err
		}

		err = pid3.Wait()
		if err != nil {
			return nil, err
		}

		err = stdin3.Close()
		if err != nil {
			return nil, err
		}

		pid4, err := ctr.Start(ctx, client.StartRequest{
			Args:   []string{"cat", "/tmp/test"},
			Stdout: &iohelper.NopWriteCloser{Writer: output},
		})
		if err != nil {
			return nil, err
		}

		err = pid4.Wait()
		if err != nil {
			return nil, err
		}

		return &client.Result{}, nil
	}

	_, err = c.Build(ctx, SolveOpt{}, product, b, nil)
	require.NoError(t, err)
	require.Equal(t, "testing", output.String())

	checkAllReleasable(t, c, sb, true)
}

// testClientGatewayContainerExecTty is testing that we can get a tty via
// executor.Exec (secondary process)
func testClientGatewayContainerExecTty(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)
	ctx := sb.Context()

	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	product := "buildkit_test"

	inputR, inputW := io.Pipe()
	output := bytes.NewBuffer(nil)
	b := func(ctx context.Context, c client.Client) (*client.Result, error) {
		ctx, timeout := context.WithTimeoutCause(ctx, 10*time.Second, nil)
		defer timeout()
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
		require.NoError(t, err)

		pid1, err := ctr.Start(ctx, client.StartRequest{
			Args: []string{"sleep", "10"},
		})
		require.NoError(t, err)

		defer pid1.Wait()
		defer ctr.Release(ctx)

		prompt := newTestPrompt(ctx, t, inputW, output)
		pid2, err := ctr.Start(ctx, client.StartRequest{
			Args:   []string{"sh"},
			Tty:    true,
			Stdin:  inputR,
			Stdout: &iohelper.NopWriteCloser{Writer: output},
			Stderr: &iohelper.NopWriteCloser{Writer: output},
			Env:    []string{fmt.Sprintf("PS1=%s", prompt.String())},
		})
		require.NoError(t, err)

		err = pid2.Resize(ctx, client.WinSize{Rows: 40, Cols: 80})
		require.NoError(t, err)
		prompt.SendExpect("ttysize", "80 40")
		prompt.Send("cd /tmp")
		prompt.SendExpect("pwd", "/tmp")
		prompt.Send("echo foobar > newfile")
		prompt.SendExpect("cat /tmp/newfile", "foobar")
		err = pid2.Resize(ctx, client.WinSize{Rows: 60, Cols: 100})
		require.NoError(t, err)
		prompt.SendExpect("ttysize", "100 60")
		prompt.SendExit(99)

		err = pid2.Wait()
		var exitError *gatewayapi.ExitError
		require.ErrorAs(t, err, &exitError)
		require.Equal(t, uint32(99), exitError.ExitCode)

		return &client.Result{}, err
	}

	_, err = c.Build(ctx, SolveOpt{}, product, b, nil)
	require.Error(t, err)
	var exitError *gatewayapi.ExitError
	require.ErrorAs(t, err, &exitError)
	require.Equal(t, uint32(99), exitError.ExitCode)
	require.Regexp(t, "exit code: 99", err.Error())

	inputW.Close()
	inputR.Close()

	checkAllReleasable(t, c, sb, true)
}

// testClientGatewayContainerPID1Exit is testing that all process started
// via `Exec` are shutdown when the primary pid1 process exits
func testClientGatewayContainerPID1Exit(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)

	ctx := sb.Context()

	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	product := "buildkit_test"

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

		start := time.Now()
		defer func() {
			// ensure pid1 and pid2 exits from cancel before the 10s sleep
			// exits naturally
			require.WithinDuration(t, start, time.Now(), 10*time.Second)
			// assert this test ran for at least one second for pid1
			lapse := time.Since(start)
			require.Greater(t, lapse.Seconds(), float64(1))
		}()

		pid1, err := ctr.Start(ctx, client.StartRequest{
			Args: []string{"sleep", "1"},
		})
		require.NoError(t, err)
		defer pid1.Wait()

		pid2, err := ctr.Start(ctx, client.StartRequest{
			Args: []string{"sleep", "10"},
		})
		require.NoError(t, err)

		return &client.Result{}, pid2.Wait()
	}

	_, err = c.Build(ctx, SolveOpt{}, product, b, nil)
	require.Error(t, err)
	var exitError *gatewayapi.ExitError
	require.ErrorAs(t, err, &exitError)
	require.Equal(t, uint32(137), exitError.ExitCode)
	// `exit code: 137` (ie sigkill)
	require.Regexp(t, "exit code: 137", err.Error())

	checkAllReleasable(t, c, sb, true)
}

// testClientGatewayContainerPID1Fail is testing clean shutdown and release
// of resources when the primary pid1 exits with non-zero exit status
func testClientGatewayContainerPID1Fail(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)

	ctx := sb.Context()

	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	product := "buildkit_test"

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

		pid1, err := ctr.Start(ctx, client.StartRequest{
			Args: []string{"sh", "-c", "exit 99"},
		})
		if err != nil {
			ctr.Release(ctx)
			return nil, err
		}

		defer ctr.Release(ctx)
		err = pid1.Wait()

		var exitError *gatewayapi.ExitError
		require.ErrorAs(t, err, &exitError)
		require.Equal(t, uint32(99), exitError.ExitCode)

		return nil, err
	}

	_, err = c.Build(ctx, SolveOpt{}, product, b, nil)
	require.Error(t, err)

	checkAllReleasable(t, c, sb, true)
}

// testClientGatewayContainerPID1Tty is testing that we can get a tty via
// a container pid1, executor.Run
func testClientGatewayContainerPID1Tty(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)
	ctx := sb.Context()

	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	product := "buildkit_test"

	inputR, inputW := io.Pipe()
	output := bytes.NewBuffer(nil)

	b := func(ctx context.Context, c client.Client) (*client.Result, error) {
		ctx, timeout := context.WithTimeoutCause(ctx, 10*time.Second, nil)
		defer timeout()

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
		require.NoError(t, err)
		defer ctr.Release(ctx)

		prompt := newTestPrompt(ctx, t, inputW, output)
		pid1, err := ctr.Start(ctx, client.StartRequest{
			Args:   []string{"sh"},
			Tty:    true,
			Stdin:  inputR,
			Stdout: &iohelper.NopWriteCloser{Writer: output},
			Stderr: &iohelper.NopWriteCloser{Writer: output},
			Env:    []string{fmt.Sprintf("PS1=%s", prompt.String())},
		})
		require.NoError(t, err)
		err = pid1.Resize(ctx, client.WinSize{Rows: 40, Cols: 80})
		require.NoError(t, err)
		prompt.SendExpect("ttysize", "80 40")
		prompt.Send("cd /tmp")
		prompt.SendExpect("pwd", "/tmp")
		prompt.Send("echo foobar > newfile")
		prompt.SendExpect("cat /tmp/newfile", "foobar")
		err = pid1.Resize(ctx, client.WinSize{Rows: 60, Cols: 100})
		require.NoError(t, err)
		prompt.SendExpect("ttysize", "100 60")
		prompt.SendExit(99)

		err = pid1.Wait()
		var exitError *gatewayapi.ExitError
		require.ErrorAs(t, err, &exitError)
		require.Equal(t, uint32(99), exitError.ExitCode)

		return &client.Result{}, err
	}

	_, err = c.Build(ctx, SolveOpt{}, product, b, nil)
	require.Error(t, err)

	inputW.Close()
	inputR.Close()

	checkAllReleasable(t, c, sb, true)
}

// testClientGatewayContainerSignal is testing that we can send a signal
func testClientGatewayContainerSignal(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)
	ctx := sb.Context()

	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	product := "buildkit_test"

	b := func(ctx context.Context, c client.Client) (*client.Result, error) {
		ctx, timeout := context.WithTimeoutCause(ctx, 10*time.Second, nil)
		defer timeout()

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

		ctr1, err := c.NewContainer(ctx, client.NewContainerRequest{
			Mounts: []client.Mount{{
				Dest:      "/",
				MountType: pb.MountType_BIND,
				Ref:       r.Ref,
			}},
		})
		require.NoError(t, err)
		defer ctr1.Release(ctx)

		pid1, err := ctr1.Start(ctx, client.StartRequest{
			Args: []string{"sh", "-c", `trap 'kill $(jobs -p); exit 99' HUP; sleep 10 & wait`},
		})
		require.NoError(t, err)

		// allow for the shell script to setup the trap before we signal it
		time.Sleep(time.Second)

		err = pid1.Signal(ctx, syscall.SIGHUP)
		require.NoError(t, err)

		err = pid1.Wait()
		var exitError *gatewayapi.ExitError
		require.ErrorAs(t, err, &exitError)
		require.Equal(t, uint32(99), exitError.ExitCode)

		// Now try again to signal an exec process

		ctr2, err := c.NewContainer(ctx, client.NewContainerRequest{
			Mounts: []client.Mount{{
				Dest:      "/",
				MountType: pb.MountType_BIND,
				Ref:       r.Ref,
			}},
		})
		require.NoError(t, err)
		defer ctr2.Release(ctx)

		pid1, err = ctr2.Start(ctx, client.StartRequest{
			Args: []string{"sleep", "10"},
		})
		require.NoError(t, err)

		pid2, err := ctr2.Start(ctx, client.StartRequest{
			Args: []string{"sh", "-c", `trap 'kill $(jobs -p); exit 111' INT; sleep 10 & wait`},
		})
		require.NoError(t, err)

		// allow for the shell script to setup the trap before we signal it
		time.Sleep(time.Second)

		err = pid2.Signal(ctx, syscall.SIGINT)
		require.NoError(t, err)

		err = pid2.Wait()
		require.ErrorAs(t, err, &exitError)
		require.Equal(t, uint32(111), exitError.ExitCode)

		pid1.Signal(ctx, syscall.SIGKILL)
		pid1.Wait()
		return &client.Result{}, err
	}

	_, err = c.Build(ctx, SolveOpt{}, product, b, nil)
	require.Error(t, err)

	checkAllReleasable(t, c, sb, true)
}

// testClientGatewayExecError is testing gateway exec to recreate the container
// process for a failed execop.
func testClientGatewayExecError(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)

	ctx := sb.Context()

	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	b := func(ctx context.Context, c client.Client) (*client.Result, error) {
		id := identity.NewID()
		tests := []struct {
			Name      string
			State     llb.State
			NumMounts int
			Paths     []string
		}{{
			"only rootfs",
			llb.Image("busybox:latest").Run(
				llb.Shlexf(`sh -c "echo %s > /data && fail"`, id),
			).Root(),
			1,
			[]string{"/data"},
		}, {
			"rootfs and readwrite scratch mount",
			llb.Image("busybox:latest").Run(
				llb.Shlexf(`sh -c "echo %s > /data && echo %s > /rw/data && fail"`, id, id),
				llb.AddMount("/rw", llb.Scratch()),
			).Root(),
			2,
			[]string{"/data", "/rw/data"},
		}, {
			"rootfs and readwrite mount",
			llb.Image("busybox:latest").Run(
				llb.Shlexf(`sh -c "echo %s > /data && echo %s > /rw/data && fail"`, id, id),
				llb.AddMount("/rw", llb.Scratch().File(llb.Mkfile("foo", 0o700, []byte(id)))),
			).Root(),
			2,
			[]string{"/data", "/rw/data", "/rw/foo"},
		}, {
			"rootfs and readonly scratch mount",
			llb.Image("busybox:latest").Run(
				llb.Shlexf(`sh -c "echo %s > /data && echo %s > /readonly/foo"`, id, id),
				llb.AddMount("/readonly", llb.Scratch(), llb.Readonly),
			).Root(),
			2,
			[]string{"/data"},
		}, {
			"rootfs and readwrite force no output mount",
			llb.Image("busybox:latest").Run(
				llb.Shlexf(`sh -c "echo %s > /data && echo %s > /rw/data && fail"`, id, id),
				llb.AddMount(
					"/rw",
					llb.Scratch().File(llb.Mkfile("foo", 0o700, []byte(id))),
					llb.ForceNoOutput,
				),
			).Root(),
			2,
			[]string{"/data", "/rw/data", "/rw/foo"},
		}}

		for _, tt := range tests {
			t.Run(tt.Name, func(t *testing.T) {
				def, err := tt.State.Marshal(ctx)
				require.NoError(t, err)

				_, solveErr := c.Solve(ctx, client.SolveRequest{
					Evaluate:   true,
					Definition: def.ToPB(),
				})
				require.Error(t, solveErr)

				var se *errdefs.SolveError
				require.ErrorAs(t, solveErr, &se)
				require.Len(t, se.InputIDs, tt.NumMounts)
				require.Len(t, se.MountIDs, tt.NumMounts)

				op := se.Op
				require.NotNil(t, op)
				require.NotNil(t, op.Op)

				opExec, ok := se.Op.Op.(*pb.Op_Exec)
				require.True(t, ok)

				exec := opExec.Exec

				var mounts []client.Mount
				for i, mnt := range exec.Mounts {
					mounts = append(mounts, client.Mount{
						Selector:  mnt.Selector,
						Dest:      mnt.Dest,
						ResultID:  se.MountIDs[i],
						Readonly:  mnt.Readonly,
						MountType: mnt.MountType,
						CacheOpt:  mnt.CacheOpt,
						SecretOpt: mnt.SecretOpt,
						SSHOpt:    mnt.SSHOpt,
					})
				}

				ctr, err := c.NewContainer(ctx, client.NewContainerRequest{
					Mounts:      mounts,
					NetMode:     exec.Network,
					Platform:    op.Platform,
					Constraints: op.Constraints,
				})
				require.NoError(t, err)
				defer ctr.Release(ctx)

				inputR, inputW := io.Pipe()
				defer inputW.Close()
				defer inputR.Close()

				pid1Output := bytes.NewBuffer(nil)

				prompt := newTestPrompt(ctx, t, inputW, pid1Output)
				pid1, err := ctr.Start(ctx, client.StartRequest{
					Args:   []string{"sh"},
					Tty:    true,
					Stdin:  inputR,
					Stdout: &iohelper.NopWriteCloser{Writer: pid1Output},
					Stderr: &iohelper.NopWriteCloser{Writer: pid1Output},
					Env:    []string{fmt.Sprintf("PS1=%s", prompt.String())},
				})
				require.NoError(t, err)

				meta := exec.Meta
				for _, p := range tt.Paths {
					output := bytes.NewBuffer(nil)
					proc, err := ctr.Start(ctx, client.StartRequest{
						Args:         []string{"cat", p},
						Env:          meta.Env,
						User:         meta.User,
						Cwd:          meta.Cwd,
						Stdout:       &iohelper.NopWriteCloser{Writer: output},
						SecurityMode: exec.Security,
					})
					require.NoError(t, err)

					err = proc.Wait()
					require.NoError(t, err)
					require.Equal(t, id, strings.TrimSpace(output.String()))
				}

				prompt.SendExit(0)
				err = pid1.Wait()
				require.NoError(t, err)
			})
		}

		return client.NewResult(), nil
	}

	_, err = c.Build(ctx, SolveOpt{}, "buildkit_test", b, nil)
	require.NoError(t, err)

	checkAllReleasable(t, c, sb, true)
}

// testClientGatewayExecFileActionError is testing gateway exec into the modified
// mount of a failed fileop during a solve.
func testClientGatewayExecFileActionError(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)

	ctx := sb.Context()

	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	b := func(ctx context.Context, c client.Client) (*client.Result, error) {
		st := llb.Image("busybox:latest")
		def, err := st.Marshal(ctx)
		require.NoError(t, err)

		res, err := c.Solve(ctx, client.SolveRequest{
			Definition: def.ToPB(),
		})
		require.NoError(t, err)

		debugfs, err := res.SingleRef()
		require.NoError(t, err)

		id := identity.NewID()
		tests := []struct {
			Name       string
			State      llb.State
			NumInputs  int
			NumOutputs int
			Path       string
		}{{
			"mkfile",
			llb.Scratch().File(
				llb.Mkdir("/found", 0o700).
					Mkfile("/found/foo", 0o600, []byte(id)).
					Mkfile("/notfound/foo", 0o600, []byte(id)),
			),
			0, 3, "/input/found/foo",
		}, {
			"copy from input",
			llb.Image("busybox").File(
				llb.Copy(
					llb.Scratch().File(
						llb.Mkdir("/foo", 0o600).Mkfile("/foo/bar", 0o700, []byte(id)),
					),
					"/foo/bar",
					"/notfound/baz",
				),
			),
			2, 1, "/secondary/foo/bar",
		}, {
			"copy from action",
			llb.Image("busybox").File(
				llb.Copy(
					llb.Mkdir("/foo", 0o600).Mkfile("/foo/bar", 0o700, []byte(id)).WithState(llb.Scratch()),
					"/foo/bar",
					"/notfound/baz",
				),
			),
			1, 3, "/secondary/foo/bar",
		}}

		for _, tt := range tests {
			t.Run(tt.Name, func(t *testing.T) {
				def, err := tt.State.Marshal(ctx)
				require.NoError(t, err)

				_, err = c.Solve(ctx, client.SolveRequest{
					Evaluate:   true,
					Definition: def.ToPB(),
				})
				require.Error(t, err)

				var se *errdefs.SolveError
				require.ErrorAs(t, err, &se)
				require.Len(t, se.InputIDs, tt.NumInputs)

				// There is one output for every action in the fileop that failed.
				require.Len(t, se.MountIDs, tt.NumOutputs)

				op, ok := se.Op.Op.(*pb.Op_File)
				require.True(t, ok)

				subject, ok := se.Subject.(*errdefs.Solve_File)
				require.True(t, ok)

				// Retrieve the action that failed from the sbuject.
				idx := subject.File.Index
				require.Less(t, int(idx), len(op.File.Actions))
				action := op.File.Actions[idx]

				// The output for a file action is mapped by its index.
				inputID := se.MountIDs[idx]

				var secondaryID string
				if action.SecondaryInput != -1 {
					// If the secondary input is a result from another exec, it will be one
					// of the input IDs, otherwise it's a intermediary mutable from another
					// action in the same fileop.
					if int(action.SecondaryInput) < len(se.InputIDs) {
						secondaryID = se.InputIDs[action.SecondaryInput]
					} else {
						secondaryID = se.MountIDs[int(action.SecondaryInput)-len(se.InputIDs)]
					}
				}

				mounts := []client.Mount{{
					Dest:      "/",
					MountType: pb.MountType_BIND,
					Ref:       debugfs,
				}, {
					Dest:      "/input",
					MountType: pb.MountType_BIND,
					ResultID:  inputID,
				}}

				if secondaryID != "" {
					mounts = append(mounts, client.Mount{
						Dest:      "/secondary",
						MountType: pb.MountType_BIND,
						ResultID:  secondaryID,
					})
				}

				ctr, err := c.NewContainer(ctx, client.NewContainerRequest{Mounts: mounts})
				require.NoError(t, err)

				// Verify that the randomly generated data can be found in a mutable ref
				// created by the actions that have succeeded.
				output := bytes.NewBuffer(nil)
				proc, err := ctr.Start(ctx, client.StartRequest{
					Args:   []string{"cat", tt.Path},
					Stdout: &iohelper.NopWriteCloser{Writer: output},
				})
				require.NoError(t, err)

				err = proc.Wait()
				require.NoError(t, err)
				require.Equal(t, id, strings.TrimSpace(output.String()))

				err = ctr.Release(ctx)
				require.NoError(t, err)
			})
		}

		return client.NewResult(), nil
	}

	_, err = c.Build(ctx, SolveOpt{}, "buildkit_test", b, nil)
	require.NoError(t, err)

	checkAllReleasable(t, c, sb, true)
}

// testClientGatewaySlowCacheExecError is testing gateway exec into the ref
// that failed to mount during an execop.
func testClientGatewaySlowCacheExecError(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)

	ctx := sb.Context()

	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	id := identity.NewID()
	input := llb.Scratch().File(
		llb.Mkdir("/found", 0o700).
			Mkfile("/found/data", 0o600, []byte(id)),
	)

	st := llb.Image("busybox:latest").Run(
		llb.Shlexf("echo hello"),
		// Only readonly mounts trigger slow cache errors.
		llb.AddMount("/src", input, llb.SourcePath("/notfound"), llb.Readonly),
	).Root()

	def, err := st.Marshal(ctx)
	require.NoError(t, err)

	b := func(ctx context.Context, c client.Client) (*client.Result, error) {
		_, solveErr := c.Solve(ctx, client.SolveRequest{
			Evaluate:   true,
			Definition: def.ToPB(),
		})
		require.Error(t, solveErr)

		var se *errdefs.SolveError
		require.ErrorAs(t, solveErr, &se)

		_, ok := se.Op.Op.(*pb.Op_Exec)
		require.True(t, ok)

		_, ok = se.Subject.(*errdefs.Solve_Cache)
		require.True(t, ok)
		// Slow cache errors should only have exactly one input and no outputs.
		require.Len(t, se.InputIDs, 1)
		require.Len(t, se.MountIDs, 0)

		st := llb.Image("busybox:latest")
		def, err := st.Marshal(ctx)
		require.NoError(t, err)

		res, err := c.Solve(ctx, client.SolveRequest{
			Definition: def.ToPB(),
		})
		require.NoError(t, err)

		ref, err := res.SingleRef()
		require.NoError(t, err)

		ctr, err := c.NewContainer(ctx, client.NewContainerRequest{
			Mounts: []client.Mount{{
				Dest:      "/",
				MountType: pb.MountType_BIND,
				Ref:       ref,
			}, {
				Dest:      "/problem",
				MountType: pb.MountType_BIND,
				ResultID:  se.InputIDs[0],
			}},
		})
		require.NoError(t, err)
		defer ctr.Release(ctx)

		output := bytes.NewBuffer(nil)
		proc, err := ctr.Start(ctx, client.StartRequest{
			Args:   []string{"cat", "/problem/found/data"},
			Stdout: &iohelper.NopWriteCloser{Writer: output},
		})
		require.NoError(t, err)

		err = proc.Wait()
		require.NoError(t, err)
		require.Equal(t, id, strings.TrimSpace(output.String()))

		return client.NewResult(), nil
	}

	_, err = c.Build(ctx, SolveOpt{}, "buildkit_test", b, nil)
	require.NoError(t, err)

	checkAllReleasable(t, c, sb, true)
}

type testPrompt struct {
	ctx    context.Context
	t      *testing.T
	output *bytes.Buffer
	input  io.Writer
	prompt string
	pos    int
}

func (p *testPrompt) String() string { return p.prompt }

func (p *testPrompt) SendExit(status int) {
	p.input.Write(fmt.Appendf(nil, "exit %d\n", status))
}

func (p *testPrompt) Send(cmd string) {
	p.input.Write([]byte(cmd + "\n"))
	p.wait(p.prompt)
}

func (p *testPrompt) SendExpect(cmd, expected string) {
	for {
		p.input.Write([]byte(cmd + "\n"))
		response := p.wait(p.prompt)
		if strings.Contains(response, expected) {
			return
		}
	}
}

func (p *testPrompt) wait(msg string) string {
	for {
		newOutput := p.output.String()[p.pos:]
		if strings.Contains(newOutput, msg) {
			p.pos += len(newOutput)
			return newOutput
		}
		select {
		case <-p.ctx.Done():
			p.t.Logf("Output at timeout: %s", p.output.String())
			p.t.Fatalf("Timeout waiting for %q", msg)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func newTestPrompt(ctx context.Context, t *testing.T, input io.Writer, output *bytes.Buffer) *testPrompt {
	return &testPrompt{
		ctx:    ctx,
		t:      t,
		input:  input,
		output: output,
		prompt: "% ",
	}
}
