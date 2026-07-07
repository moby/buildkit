package client

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/secrets/secretsprovider"
	"github.com/moby/buildkit/session/sshforward/sshprovider"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/iohelper"
	utilsystem "github.com/moby/buildkit/util/system"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	"github.com/tonistiigi/fsutil"
	"golang.org/x/crypto/ssh/agent"
)

// testClientGatewayContainerMounts is testing mounts derived from various
// llb.States
func testClientGatewayContainerMounts(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)

	ctx := sb.Context()

	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	tmpdir := integration.Tmpdir(t)

	err = os.WriteFile(filepath.Join(tmpdir.Name, "local-file"), []byte("local"), 0o644)
	require.NoError(t, err)

	a := agent.NewKeyring()
	sockPath, err := makeSSHAgentSock(t, a)
	require.NoError(t, err)

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

		containerMounts := []client.Mount{
			{
				Dest:      "/",
				MountType: pb.MountType_BIND,
			},
			{
				Dest:      "/foo",
				MountType: pb.MountType_BIND,
			},
			{
				Dest:      "/local",
				MountType: pb.MountType_BIND,
			},
			{
				Dest:      "/cached",
				MountType: pb.MountType_CACHE,
				CacheOpt: &pb.CacheOpt{
					ID:      t.Name(),
					Sharing: pb.CacheSharingOpt_SHARED,
				},
			},
			{
				Dest:      "/tmpfs",
				MountType: pb.MountType_TMPFS,
			},
			{
				Dest:      "/run/secrets/mysecret",
				MountType: pb.MountType_SECRET,
				SecretOpt: &pb.SecretOpt{
					ID: "/run/secrets/mysecret",
				},
			},
			{
				Dest:      sockPath,
				MountType: pb.MountType_SSH,
				SSHOpt: &pb.SSHOpt{
					ID: t.Name(),
				},
			},
		}

		// Fill in mount references.
		for i, m := range containerMounts {
			st := mounts[m.Dest]

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
			containerMounts[i].Ref = r.Ref
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

		files := []struct {
			index int
			path  string
			data  []byte
		}{
			{0, "/root-file", []byte(nil)},
			{1, "/foo-file", []byte(nil)},
			{2, "/local-file", []byte(`local`)},
			{3, "/cache-file", []byte(nil)},
		}
		for _, file := range files {
			cpath := containerMounts[file.index].Dest + file.path
			pid, err := ctr.Start(ctx, client.StartRequest{
				Args: []string{"test", "-f", cpath},
			})
			require.NoError(t, err, "cannot start container to check for file: %s", cpath)
			err = pid.Wait()
			require.NoError(t, err, "process for checking file failed: %s", cpath)

			_, err = ctr.StatFile(ctx, client.StatContainerRequest{
				StatRequest: client.StatRequest{
					Path: file.path,
				},
				MountIndex: file.index,
			})
			require.NoError(t, err, "stat file for %q on mount %d failed", file.path, file.index)

			b, err := ctr.ReadFile(ctx, client.ReadContainerRequest{
				ReadRequest: client.ReadRequest{
					Filename: file.path,
				},
				MountIndex: file.index,
			})
			require.NoError(t, err, "read file for %q on mount %d failed", file.path, file.index)
			require.Equal(t, file.data, b)
		}

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
			Stdout: &iohelper.NopWriteCloser{Writer: secretOutput},
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
		LocalMounts: map[string]fsutil.FS{
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

// testClientGatewayContainerPlatformPATH is testing the correct default PATH
// gets set for the requested platform
func testClientGatewayContainerPlatformPATH(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)
	ctx := sb.Context()

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
					Stdout: &iohelper.NopWriteCloser{Writer: output},
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

func testClientGatewayContainerSecretEnv(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)

	ctx := sb.Context()

	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	product := "buildkit_test"

	b := func(ctx context.Context, c client.Client) (*client.Result, error) {
		mounts := map[string]llb.State{
			"/": llb.Image("busybox:latest"),
		}

		var containerMounts []client.Mount
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

		pid, err := ctr.Start(ctx, client.StartRequest{
			Args: []string{"sh", "-c", "test $SOME_SECRET = foo-secret"},
			SecretEnv: []*pb.SecretEnv{
				{
					ID:   "sekrit",
					Name: "SOME_SECRET",
				},
			},
		})
		require.NoError(t, err)
		err = pid.Wait()
		require.NoError(t, err)

		return &client.Result{}, ctr.Release(ctx)
	}

	_, err = c.Build(ctx, SolveOpt{
		Session: []session.Attachable{
			secretsprovider.FromMap(map[string][]byte{
				"sekrit": []byte("foo-secret"),
			}),
		},
	}, product, b, nil)
	require.NoError(t, err)

	checkAllReleasable(t, c, sb, true)
}
