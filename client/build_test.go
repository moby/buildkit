package client

import (
	"bytes"
	"context"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/frontend/gateway/client"
	gatewayapi "github.com/moby/buildkit/frontend/gateway/pb"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/solver/errdefs"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestClientGatewayIntegration(t *testing.T) {
	integration.Run(t, []integration.Test{
		testClientGatewaySolve,
		testClientGatewayFailedSolve,
		testClientGatewayEmptySolve,
		testNoBuildID,
		testUnknownBuildID,
		testClientGatewayContainerExecPipe,
		testClientGatewayContainerCancelOnRelease,
		testClientGatewayContainerPID1Fail,
		testClientGatewayContainerPID1Exit,
		testClientGatewayContainerMounts,
	}, integration.WithMirroredImages(integration.OfficialImages("busybox:latest")))
}

func testClientGatewaySolve(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)

	ctx := context.TODO()

	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	product := "buildkit_test"
	optKey := "test-string"

	b := func(ctx context.Context, c client.Client) (*client.Result, error) {
		if c.BuildOpts().Product != product {
			return nil, errors.Errorf("expected product %q, got %q", product, c.BuildOpts().Product)
		}
		opts := c.BuildOpts().Opts
		testStr, ok := opts[optKey]
		if !ok {
			return nil, errors.Errorf(`build option %q missing`, optKey)
		}

		run := llb.Image("busybox:latest").Run(
			llb.ReadonlyRootFS(),
			llb.Args([]string{"/bin/sh", "-ec", `echo -n '` + testStr + `' > /out/foo`}),
		)
		st := run.AddMount("/out", llb.Scratch())

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

		read, err := r.Ref.ReadFile(ctx, client.ReadRequest{
			Filename: "/foo",
		})
		if err != nil {
			return nil, errors.Wrap(err, "failed to read result")
		}
		if testStr != string(read) {
			return nil, errors.Errorf("read back %q, expected %q", string(read), testStr)
		}
		return r, nil
	}

	tmpdir, err := ioutil.TempDir("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(tmpdir)

	testStr := "This is a test"

	_, err = c.Build(ctx, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: tmpdir,
			},
		},
		FrontendAttrs: map[string]string{
			optKey: testStr,
		},
	}, product, b, nil)
	require.NoError(t, err)

	read, err := ioutil.ReadFile(filepath.Join(tmpdir, "foo"))
	require.NoError(t, err)
	require.Equal(t, testStr, string(read))

	checkAllReleasable(t, c, sb, true)
}

func testClientGatewayFailedSolve(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)

	ctx := context.TODO()

	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	b := func(ctx context.Context, c client.Client) (*client.Result, error) {
		return nil, errors.New("expected to fail")
	}

	_, err = c.Build(ctx, SolveOpt{}, "", b, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "expected to fail")
}

func testClientGatewayEmptySolve(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)

	ctx := context.TODO()

	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	b := func(ctx context.Context, c client.Client) (*client.Result, error) {
		r, err := c.Solve(ctx, client.SolveRequest{})
		if err != nil {
			return nil, errors.Wrap(err, "failed to solve")
		}
		if r.Ref != nil || r.Refs != nil || r.Metadata != nil {
			return nil, errors.Errorf("got unexpected non-empty result %+v", r)
		}
		return r, nil
	}

	_, err = c.Build(ctx, SolveOpt{}, "", b, nil)
	require.NoError(t, err)
}

func testNoBuildID(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)

	ctx := context.TODO()

	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	g := gatewayapi.NewLLBBridgeClient(c.conn)
	_, err = g.Ping(ctx, &gatewayapi.PingRequest{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "no buildid found in context")
}

func testUnknownBuildID(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)

	ctx := context.TODO()

	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	g := c.gatewayClientForBuild(t.Name() + identity.NewID())
	_, err = g.Ping(ctx, &gatewayapi.PingRequest{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "no such job")
}

// testClientGatewayContainerCancelOnRelease is testing that all running
// processes are terminated when the container is released.
func testClientGatewayContainerCancelOnRelease(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)

	ctx := context.TODO()

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
			Cwd:  "/",
		})
		require.NoError(t, err)

		pid2, err := ctr.Start(ctx, client.StartRequest{
			Args: []string{"sleep", "10"},
			Cwd:  "/",
		})
		require.NoError(t, err)

		ctr.Release(ctx)
		err = pid1.Wait()
		require.Contains(t, err.Error(), context.Canceled.Error())

		err = pid2.Wait()
		require.Contains(t, err.Error(), context.Canceled.Error())

		return &client.Result{}, nil
	}

	c.Build(ctx, SolveOpt{}, product, b, nil)
	checkAllReleasable(t, c, sb, true)
}

// testClientGatewayContainerExecPipe is testing the ability to pipe multiple
// process together all started via `Exec` into the same container.
// We are mimicing: `echo testing | cat | cat > /tmp/foo && cat /tmp/foo`
func testClientGatewayContainerExecPipe(t *testing.T, sb integration.Sandbox) {
	if sb.Rootless() {
		// TODO fix this
		// We get `panic: cannot statfs cgroup root` from runc when when running
		// this test with runc-rootless, no idea why.
		t.Skip("Skipping oci-rootless for cgroup error")
	}
	requiresLinux(t)

	ctx := context.TODO()

	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	product := "buildkit_test"

	output := bytes.NewBuffer([]byte{})

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
		pid1, err := ctr.Start(ctx, client.StartRequest{
			Args: []string{"sleep", "10"},
			Cwd:  "/",
		})
		if err != nil {
			ctr.Release(ctx)
			return nil, err
		}

		defer func() {
			// cancel pid1
			ctr.Release(ctx)
			pid1.Wait()
		}()

		// first part is `echo testing | cat`
		stdin2 := bytes.NewBuffer([]byte("testing"))
		stdin3, stdout2 := io.Pipe()

		pid2, err := ctr.Start(ctx, client.StartRequest{
			Args:   []string{"cat"},
			Cwd:    "/",
			Tty:    false,
			Stdin:  ioutil.NopCloser(stdin2),
			Stdout: stdout2,
		})

		if err != nil {
			return nil, err
		}

		// next part is: `| cat > /tmp/test`
		pid3, err := ctr.Start(ctx, client.StartRequest{
			Args:  []string{"sh", "-c", "cat > /tmp/test"},
			Cwd:   "/",
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
			Cwd:    "/",
			Stdout: &nopCloser{output},
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

// testClientGatewayContainerPID1Fail is testing clean shutdown and release
// of resources when the primary pid1 exits with non-zero exit status
func testClientGatewayContainerPID1Fail(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)

	ctx := context.TODO()

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
			Args: []string{"false"},
			Cwd:  "/",
		})
		if err != nil {
			ctr.Release(ctx)
			return nil, err
		}

		defer ctr.Release(ctx)
		err = pid1.Wait()

		var exitError *errdefs.ExitError
		require.True(t, errors.As(err, &exitError))
		require.Equal(t, uint32(1), exitError.ExitCode)

		return nil, err
	}

	_, err = c.Build(ctx, SolveOpt{}, product, b, nil)
	require.Error(t, err)

	checkAllReleasable(t, c, sb, true)
}

// testClientGatewayContainerPID1Exit is testing that all process started
// via `Exec` are shutdown when the primary pid1 process exits
func testClientGatewayContainerPID1Exit(t *testing.T, sb integration.Sandbox) {
	if sb.Rootless() {
		// TODO fix this
		// We get `panic: cannot statfs cgroup root` when running this test
		// with runc-rootless
		t.Skip("Skipping runc-rootless for cgroup error")
	}
	requiresLinux(t)

	ctx := context.TODO()

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
			lapse := time.Now().Sub(start)
			require.Greater(t, lapse.Seconds(), float64(1))
		}()

		pid1, err := ctr.Start(ctx, client.StartRequest{
			Args: []string{"sleep", "1"},
			Cwd:  "/",
		})
		require.NoError(t, err)
		defer pid1.Wait()

		pid2, err := ctr.Start(ctx, client.StartRequest{
			Args: []string{"sleep", "10"},
			Cwd:  "/",
		})
		require.NoError(t, err)

		return &client.Result{}, pid2.Wait()
	}

	_, err = c.Build(ctx, SolveOpt{}, product, b, nil)
	// pid2 should error with `exit code: 255 on runc or
	// `exit code: 137` (ie sigkill) on containerd
	require.Error(t, err)
	require.Regexp(t, "exit code: (255|137)", err.Error())

	checkAllReleasable(t, c, sb, true)
}

// testClientGatewayContainerMounts is testing mounts derived from various
// llb.States
func testClientGatewayContainerMounts(t *testing.T, sb integration.Sandbox) {
	if sb.Rootless() {
		// TODO fix this
		// We get `panic: cannot statfs cgroup root` when running this test
		// with runc-rootless
		t.Skip("Skipping runc-rootless for cgroup error")
	}
	requiresLinux(t)

	ctx := context.TODO()

	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	tmpdir, err := ioutil.TempDir("", "buildkit-buildctl")
	require.NoError(t, err)
	defer os.RemoveAll(tmpdir)

	err = ioutil.WriteFile(filepath.Join(tmpdir, "local-file"), []byte("local"), 0644)
	require.NoError(t, err)

	product := "buildkit_test"

	b := func(ctx context.Context, c client.Client) (*client.Result, error) {
		mounts := map[string]llb.State{
			"/": llb.Image("busybox:latest").Run(
				llb.Shlex("touch /root-file /cached/cache-file"),
				llb.AddMount("/cached", llb.Scratch(), llb.AsPersistentCacheDir(t.Name(), llb.CacheMountShared)),
			).Root(),
			"/foo": llb.Image("busybox:latest").Run(
				llb.Shlex("touch foo-file"),
				llb.Dir("/tmp"),
				llb.AddMount("/tmp", llb.Scratch()),
			).GetMount("/tmp"),
			"/local": llb.Local("mylocal"),
			// TODO How do we get a results.Ref for a cache mount, tmpfs mount
		}

		containerMounts := []client.Mount{{
			Dest:      "/cached",
			MountType: pb.MountType_CACHE,
			CacheOpt: &pb.CacheOpt{
				ID:      t.Name(),
				Sharing: pb.CacheSharingOpt_SHARED,
			},
		}, {
			Dest:      "/tmpfs",
			MountType: pb.MountType_TMPFS,
		}}

		for mountpoint, st := range mounts {
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
			containerMounts = append(containerMounts, client.Mount{
				Dest:      mountpoint,
				MountType: pb.MountType_BIND,
				Ref:       r.Ref,
			})
		}

		ctr, err := c.NewContainer(ctx, client.NewContainerRequest{Mounts: containerMounts})
		if err != nil {
			return nil, err
		}

		pid1, err := ctr.Start(ctx, client.StartRequest{
			Args: []string{"sleep", "10"},
			Cwd:  "/",
		})
		require.NoError(t, err)
		defer pid1.Wait()

		pid, err := ctr.Start(ctx, client.StartRequest{
			Args: []string{"test", "-f", "/root-file"},
			Cwd:  "/",
		})
		require.NoError(t, err)
		err = pid.Wait()
		require.NoError(t, err)

		pid, err = ctr.Start(ctx, client.StartRequest{
			Args: []string{"test", "-f", "/foo/foo-file"},
			Cwd:  "/",
		})
		require.NoError(t, err)
		err = pid.Wait()
		require.NoError(t, err)

		pid, err = ctr.Start(ctx, client.StartRequest{
			Args: []string{"test", "-f", "/local/local-file"},
			Cwd:  "/",
		})
		require.NoError(t, err)
		err = pid.Wait()
		require.NoError(t, err)

		pid, err = ctr.Start(ctx, client.StartRequest{
			Args: []string{"test", "-f", "/cached/cache-file"},
			Cwd:  "/",
		})
		require.NoError(t, err)
		err = pid.Wait()
		require.NoError(t, err)

		pid, err = ctr.Start(ctx, client.StartRequest{
			Args: []string{"test", "-w", "/tmpfs"},
			Cwd:  "/",
		})
		require.NoError(t, err)
		err = pid.Wait()
		require.NoError(t, err)

		return &client.Result{}, ctr.Release(ctx)
	}

	_, err = c.Build(ctx, SolveOpt{
		LocalDirs: map[string]string{
			"mylocal": tmpdir,
		},
	}, product, b, nil)
	require.Contains(t, err.Error(), context.Canceled.Error())

	checkAllReleasable(t, c, sb, true)
}

type nopCloser struct {
	io.Writer
}

func (n *nopCloser) Close() error {
	return nil
}
