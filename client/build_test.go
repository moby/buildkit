package client

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/frontend/gateway/client"
	gatewayapi "github.com/moby/buildkit/frontend/gateway/pb"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/secrets/secretsprovider"
	"github.com/moby/buildkit/session/sshforward/sshprovider"
	"github.com/moby/buildkit/solver/errdefs"
	"github.com/moby/buildkit/solver/pb"
	utilsystem "github.com/moby/buildkit/util/system"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh/agent"
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
		testClientGatewayContainerPID1Tty,
		testClientGatewayContainerExecTty,
		testClientSlowCacheRootfsRef,
		testClientGatewayContainerPlatformPATH,
		testClientGatewayExecError,
		testClientGatewaySlowCacheExecError,
		testClientGatewayFileActionError,
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

// testClientGatewayContainerExecPipe is testing the ability to pipe multiple
// process together all started via `Exec` into the same container.
// We are mimicing: `echo testing | cat | cat > /tmp/foo && cat /tmp/foo`
func testClientGatewayContainerExecPipe(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)

	ctx := context.TODO()

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
		pid1, err := ctr.Start(ctx, client.StartRequest{
			Args: []string{"sleep", "10"},
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
			Args: []string{"sh", "-c", "exit 99"},
		})
		if err != nil {
			ctr.Release(ctx)
			return nil, err
		}

		defer ctr.Release(ctx)
		err = pid1.Wait()

		var exitError *errdefs.ExitError
		require.True(t, errors.As(err, &exitError))
		require.Equal(t, uint32(99), exitError.ExitCode)

		return nil, err
	}

	_, err = c.Build(ctx, SolveOpt{}, product, b, nil)
	require.Error(t, err)

	checkAllReleasable(t, c, sb, true)
}

// testClientGatewayContainerPID1Exit is testing that all process started
// via `Exec` are shutdown when the primary pid1 process exits
func testClientGatewayContainerPID1Exit(t *testing.T, sb integration.Sandbox) {
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
	// `exit code: 137` (ie sigkill)
	require.Regexp(t, "exit code: 137", err.Error())

	checkAllReleasable(t, c, sb, true)
}

// testClientGatewayContainerMounts is testing mounts derived from various
// llb.States
func testClientGatewayContainerMounts(t *testing.T, sb integration.Sandbox) {
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

	a := agent.NewKeyring()
	sockPath, clean, err := makeSSHAgentSock(a)
	require.NoError(t, err)
	defer clean()

	ssh, err := sshprovider.NewSSHAgentProvider([]sshprovider.AgentConfig{{
		ID:    t.Name(),
		Paths: []string{sockPath},
	}})
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
		}, {
			Dest:      "/run/secrets/mysecret",
			MountType: pb.MountType_SECRET,
			SecretOpt: &pb.SecretOpt{
				ID: "/run/secrets/mysecret",
			},
		}, {
			Dest:      sockPath,
			MountType: pb.MountType_SSH,
			SSHOpt: &pb.SSHOpt{
				ID: t.Name(),
			},
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
			Args:   []string{"sleep", "10"},
			Stderr: os.Stderr,
		})
		require.NoError(t, err)
		defer pid1.Wait()

		pid, err := ctr.Start(ctx, client.StartRequest{
			Args: []string{"test", "-f", "/root-file"},
		})
		require.NoError(t, err)
		err = pid.Wait()
		require.NoError(t, err)

		pid, err = ctr.Start(ctx, client.StartRequest{
			Args: []string{"test", "-f", "/foo/foo-file"},
		})
		require.NoError(t, err)
		err = pid.Wait()
		require.NoError(t, err)

		pid, err = ctr.Start(ctx, client.StartRequest{
			Args: []string{"test", "-f", "/local/local-file"},
		})
		require.NoError(t, err)
		err = pid.Wait()
		require.NoError(t, err)

		pid, err = ctr.Start(ctx, client.StartRequest{
			Args: []string{"test", "-f", "/cached/cache-file"},
		})
		require.NoError(t, err)
		err = pid.Wait()
		require.NoError(t, err)

		pid, err = ctr.Start(ctx, client.StartRequest{
			Args: []string{"test", "-w", "/tmpfs"},
		})
		require.NoError(t, err)
		err = pid.Wait()
		require.NoError(t, err)

		secretOutput := bytes.NewBuffer(nil)
		pid, err = ctr.Start(ctx, client.StartRequest{
			Args:   []string{"cat", "/run/secrets/mysecret"},
			Stdout: &nopCloser{secretOutput},
		})
		require.NoError(t, err)
		err = pid.Wait()
		require.NoError(t, err)
		require.Equal(t, "foo-secret", secretOutput.String())

		pid, err = ctr.Start(ctx, client.StartRequest{
			Args: []string{"test", "-S", sockPath},
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
		Session: []session.Attachable{
			ssh,
			secretsprovider.FromMap(map[string][]byte{
				"/run/secrets/mysecret": []byte("foo-secret"),
			}),
		},
	}, product, b, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), context.Canceled.Error())

	checkAllReleasable(t, c, sb, true)
}

// testClientGatewayContainerPID1Tty is testing that we can get a tty via
// a container pid1, executor.Run
func testClientGatewayContainerPID1Tty(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)
	ctx := context.TODO()

	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	product := "buildkit_test"

	inputR, inputW := io.Pipe()
	output := bytes.NewBuffer(nil)

	b := func(ctx context.Context, c client.Client) (*client.Result, error) {
		ctx, timeout := context.WithTimeout(ctx, 10*time.Second)
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
			Stdout: &nopCloser{output},
			Stderr: &nopCloser{output},
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
		var exitError *errdefs.ExitError
		require.True(t, errors.As(err, &exitError))
		require.Equal(t, uint32(99), exitError.ExitCode)

		return &client.Result{}, err
	}

	_, err = c.Build(ctx, SolveOpt{}, product, b, nil)
	require.Error(t, err)

	inputW.Close()
	inputR.Close()

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

func newTestPrompt(ctx context.Context, t *testing.T, input io.Writer, output *bytes.Buffer) *testPrompt {
	return &testPrompt{
		ctx:    ctx,
		t:      t,
		input:  input,
		output: output,
		prompt: "% ",
	}
}

func (p *testPrompt) String() string { return p.prompt }

func (p *testPrompt) SendExit(status int) {
	p.input.Write([]byte(fmt.Sprintf("exit %d\n", status)))
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

// testClientGatewayContainerExecTty is testing that we can get a tty via
// executor.Exec (secondary process)
func testClientGatewayContainerExecTty(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)
	ctx := context.TODO()

	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	product := "buildkit_test"

	inputR, inputW := io.Pipe()
	output := bytes.NewBuffer(nil)
	b := func(ctx context.Context, c client.Client) (*client.Result, error) {
		ctx, timeout := context.WithTimeout(ctx, 10*time.Second)
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
			Stdout: &nopCloser{output},
			Stderr: &nopCloser{output},
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
		var exitError *errdefs.ExitError
		require.True(t, errors.As(err, &exitError))
		require.Equal(t, uint32(99), exitError.ExitCode)

		return &client.Result{}, err
	}

	_, err = c.Build(ctx, SolveOpt{}, product, b, nil)
	require.Error(t, err)
	require.Regexp(t, "exit code: 99", err.Error())

	inputW.Close()
	inputR.Close()

	checkAllReleasable(t, c, sb, true)
}

func testClientSlowCacheRootfsRef(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)

	ctx := context.TODO()

	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	b := func(ctx context.Context, c client.Client) (*client.Result, error) {
		id := identity.NewID()
		input := llb.Scratch().File(
			llb.Mkdir("/found", 0700).
				Mkfile("/found/data", 0600, []byte(id)),
		)

		st := llb.Image("busybox:latest").Run(
			llb.Shlexf("echo hello"),
			// Only readonly mounts trigger slow cache errors.
			llb.AddMount("/src", input, llb.SourcePath("/notfound"), llb.Readonly),
		).Root()

		def1, err := st.Marshal(ctx)
		require.NoError(t, err)

		res1, err := c.Solve(ctx, client.SolveRequest{
			Definition: def1.ToPB(),
		})
		require.NoError(t, err)

		ref1, err := res1.SingleRef()
		require.NoError(t, err)

		// First stat should error because unlazy-ing the reference causes an error
		// in CalcSlowCache.
		_, err = ref1.StatFile(ctx, client.StatRequest{
			Path: ".",
		})
		require.Error(t, err)

		def2, err := llb.Image("busybox:latest").Marshal(ctx)
		require.NoError(t, err)

		res2, err := c.Solve(ctx, client.SolveRequest{
			Definition: def2.ToPB(),
		})
		require.NoError(t, err)

		ref2, err := res2.SingleRef()
		require.NoError(t, err)

		// Second stat should not error because the rootfs for `busybox` should not
		// have been released.
		_, err = ref2.StatFile(ctx, client.StatRequest{
			Path: ".",
		})
		require.NoError(t, err)

		return res2, nil
	}

	_, err = c.Build(ctx, SolveOpt{}, "buildkit_test", b, nil)
	require.NoError(t, err)
	checkAllReleasable(t, c, sb, true)
}

// testClientGatewayContainerPlatformPATH is testing the correct default PATH
// gets set for the requested platform
func testClientGatewayContainerPlatformPATH(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)
	ctx := context.TODO()

	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	product := "buildkit_test"
	b := func(ctx context.Context, c client.Client) (*client.Result, error) {
		st := llb.Image("busybox:latest")
		def, err := st.Marshal(ctx)
		require.NoError(t, err)
		r, err := c.Solve(ctx, client.SolveRequest{
			Definition: def.ToPB(),
		})
		require.NoError(t, err)

		tests := []struct {
			Name     string
			Platform *pb.Platform
			Expected string
		}{{
			"default path",
			nil,
			utilsystem.DefaultPathEnvUnix,
		}, {
			"linux path",
			&pb.Platform{OS: "linux"},
			utilsystem.DefaultPathEnvUnix,
		}, {
			"windows path",
			&pb.Platform{OS: "windows"},
			utilsystem.DefaultPathEnvWindows,
		}}

		for _, tt := range tests {
			t.Run(tt.Name, func(t *testing.T) {
				ctr, err := c.NewContainer(ctx, client.NewContainerRequest{
					Mounts: []client.Mount{{
						Dest:      "/",
						MountType: pb.MountType_BIND,
						Ref:       r.Ref,
					}},
					Platform: tt.Platform,
				})
				require.NoError(t, err)
				output := bytes.NewBuffer(nil)
				pid1, err := ctr.Start(ctx, client.StartRequest{
					Args:   []string{"/bin/sh", "-c", "echo -n $PATH"},
					Stdout: &nopCloser{output},
				})
				require.NoError(t, err)

				err = pid1.Wait()
				require.NoError(t, err)
				require.Equal(t, tt.Expected, output.String())
				err = ctr.Release(ctx)
				require.NoError(t, err)
			})
		}
		return &client.Result{}, err
	}

	_, err = c.Build(ctx, SolveOpt{}, product, b, nil)
	require.NoError(t, err)
	checkAllReleasable(t, c, sb, true)
}

// testClientGatewayExecError is testing gateway exec to recreate the container
// process for a failed execop.
func testClientGatewayExecError(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)

	ctx := context.TODO()

	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	id := identity.NewID()
	st := llb.Image("busybox:latest").Run(
		llb.Dir("/src"),
		llb.AddMount("/src", llb.Scratch()),
		llb.Shlexf("sh -c \"echo %s > output && fail\"", id),
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
		require.True(t, errors.As(solveErr, &se))

		solveExec, ok := se.Solve.Op.Op.(*pb.Op_Exec)
		require.True(t, ok)

		exec := solveExec.Exec

		var mounts []client.Mount
		for _, mnt := range exec.Mounts {
			mounts = append(mounts, client.Mount{
				Selector:  mnt.Selector,
				Dest:      mnt.Dest,
				ResultID:  se.Solve.OutputIDs[mnt.Output],
				Readonly:  mnt.Readonly,
				MountType: mnt.MountType,
				CacheOpt:  mnt.CacheOpt,
				SecretOpt: mnt.SecretOpt,
				SSHOpt:    mnt.SSHOpt,
			})
		}

		ctr, err := c.NewContainer(ctx, client.NewContainerRequest{
			Mounts:  mounts,
			NetMode: exec.Network,
		})
		require.NoError(t, err)
		defer ctr.Release(ctx)

		meta := exec.Meta
		output := bytes.NewBuffer(nil)

		proc, err := ctr.Start(ctx, client.StartRequest{
			Args:         []string{"cat", "output"},
			Env:          meta.Env,
			User:         meta.User,
			Cwd:          meta.Cwd,
			Stdout:       &nopCloser{output},
			SecurityMode: exec.Security,
		})
		require.NoError(t, err)

		err = proc.Wait()
		require.NoError(t, err)
		require.Equal(t, id, strings.TrimSpace(output.String()))

		return nil, solveErr
	}

	_, err = c.Build(ctx, SolveOpt{}, "buildkit_test", b, nil)
	require.Error(t, err)

	checkAllReleasable(t, c, sb, true)
}

// testClientGatewaySlowCacheExecError is testing gateway exec into the ref
// that failed to mount during an execop.
func testClientGatewaySlowCacheExecError(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)

	ctx := context.TODO()

	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	id := identity.NewID()
	input := llb.Scratch().File(
		llb.Mkdir("/found", 0700).
			Mkfile("/found/data", 0600, []byte(id)),
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
		require.True(t, errors.As(solveErr, &se))

		_, ok := se.Solve.Op.Op.(*pb.Op_Exec)
		require.True(t, ok)

		_, ok = se.Solve.Subject.(*errdefs.Solve_Cache)
		require.True(t, ok)
		// Slow cache errors should only have exactly one input and no outputs.
		require.Len(t, se.Solve.InputIDs, 1)
		require.Len(t, se.Solve.OutputIDs, 0)

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
				ResultID:  se.Solve.InputIDs[0],
			}},
		})
		require.NoError(t, err)
		defer ctr.Release(ctx)

		output := bytes.NewBuffer(nil)
		proc, err := ctr.Start(ctx, client.StartRequest{
			Args:   []string{"cat", "/problem/found/data"},
			Stdout: &nopCloser{output},
		})
		require.NoError(t, err)

		err = proc.Wait()
		require.NoError(t, err)
		require.Equal(t, id, strings.TrimSpace(output.String()))

		return nil, solveErr
	}

	_, err = c.Build(ctx, SolveOpt{}, "buildkit_test", b, nil)
	require.Error(t, err)

	checkAllReleasable(t, c, sb, true)
}

// testClientGatewayFileActionError is testing gateway exec into the modified
// mount of a failed fileop action during a solve.
func testClientGatewayFileActionError(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)

	ctx := context.TODO()

	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	id := identity.NewID()
	st := llb.Scratch().File(
		llb.Mkdir("/found", 0700).
			Mkfile("/found/foo", 0600, []byte(id)).
			Mkfile("/notfound/foo", 0600, []byte(id)),
	)

	def, err := st.Marshal(ctx)
	require.NoError(t, err)

	b := func(ctx context.Context, c client.Client) (*client.Result, error) {
		_, solveErr := c.Solve(ctx, client.SolveRequest{
			Evaluate:   true,
			Definition: def.ToPB(),
		})
		require.Error(t, solveErr)

		var se *errdefs.SolveError
		require.True(t, errors.As(solveErr, &se))
		// There are no inputs because rootfs is scratch.
		require.Len(t, se.Solve.InputIDs, 0)
		// There is one output for every action (3).
		require.Len(t, se.Solve.OutputIDs, 3)

		op, ok := se.Solve.Op.Op.(*pb.Op_File)
		require.True(t, ok)

		subject, ok := se.Solve.Subject.(*errdefs.Solve_File)
		require.True(t, ok)

		idx := subject.File.Index
		require.Less(t, int(idx), len(op.File.Actions))

		// Verify the index is pointing to the action that failed.
		action := op.File.Actions[idx]
		mkfile, ok := action.Action.(*pb.FileAction_Mkfile)
		require.True(t, ok)
		require.Equal(t, mkfile.Mkfile.Path, "/notfound/foo")

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
				ResultID:  se.Solve.OutputIDs[idx],
			}},
		})
		require.NoError(t, err)
		defer ctr.Release(ctx)

		// Verify that other actions have succeeded.
		output := bytes.NewBuffer(nil)
		proc, err := ctr.Start(ctx, client.StartRequest{
			Args:   []string{"cat", "/problem/found/foo"},
			Cwd:    "/",
			Stdout: &nopCloser{output},
		})
		require.NoError(t, err)

		err = proc.Wait()
		require.NoError(t, err)
		require.Equal(t, id, strings.TrimSpace(output.String()))

		return nil, solveErr
	}

	_, err = c.Build(ctx, SolveOpt{}, "buildkit_test", b, nil)
	require.Error(t, err)

	checkAllReleasable(t, c, sb, true)
}

type nopCloser struct {
	io.Writer
}

func (n *nopCloser) Close() error {
	return nil
}
