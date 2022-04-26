package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"syscall"

	dockerfile "github.com/moby/buildkit/frontend/dockerfile/builder"
	gwclient "github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/frontend/gateway/grpcclient"
	"github.com/moby/buildkit/solver/errdefs"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/appcontext"
)

func main() {
	var help bool
	flag.BoolVar(&help, "help", false, "show help")
	flag.Parse()
	if help {
		fmt.Printf("%s should be called from BuildKit as a frontend\n", os.Args[0])
		os.Exit(0)
	}
	if err := grpcclient.RunFromEnvironment(appcontext.Context(), withDebug(dockerfile.Build)); err != nil {
		fmt.Printf("fatal error: %+v\n", err)
		panic(err)
	}
}

func withDebug(f gwclient.BuildFunc) gwclient.BuildFunc {
	return func(ctx context.Context, c gwclient.Client) (*gwclient.Result, error) {
		res, err := f(ctx, &evaluateClient{c})
		if err == nil {
			return res, nil
		}

		// Build failed. Launch shell on the failed step

		var se *errdefs.SolveError
		if !errors.As(err, &se) {
			return nil, fmt.Errorf("err is not SolveError: %w", err)
		}
		var processResizeFn func(ctx context.Context, size gwclient.WinSize) error
		resizeFn := func(ctx context.Context, size gwclient.WinSize) error {
			if processResizeFn != nil {
				return processResizeFn(ctx, size)
			}
			return nil
		}
		var processSignalFn func(ctx context.Context, sig syscall.Signal) error
		signalFn := func(ctx context.Context, sig syscall.Signal) error {
			if processSignalFn != nil {
				return processSignalFn(ctx, sig)
			}
			return nil
		}
		stdin, stdout, stderr, ioDone, sErr := c.SessionIO(ctx, signalFn, resizeFn)
		if sErr != nil {
			return nil, fmt.Errorf("session cannot retrieved: %v: %w", sErr, err)
		}
		defer ioDone()
		fmt.Fprintf(stdout, "\n========= launching shell on a failed step =========\n")
		defer fmt.Fprintf(stdout, "\n====================================================\n")
		proc, done, execErr := execContainer(ctx, c, se.Solve.Op, se.Solve.MountIDs, stdin, stdout, stderr)
		if execErr != nil {
			return nil, fmt.Errorf("exec error: %v: %w", execErr, err)
		}
		processResizeFn = proc.Resize
		processSignalFn = proc.Signal
		if wErr := proc.Wait(); wErr != nil {
			fmt.Fprintf(stdout, "\nprocess execution failed: %v\n", wErr)
		}
		done(ctx)
		return nil, err
	}
}

func execContainer(ctx context.Context, c gwclient.Client, op *pb.Op, mountIDs []string, stdin io.ReadCloser, stdout, stderr io.WriteCloser) (gwclient.ContainerProcess, func(context.Context) error, error) {
	var exec *pb.ExecOp
	switch op := op.GetOp().(type) {
	case *pb.Op_Exec:
		exec = op.Exec
	default:
		return nil, nil, fmt.Errorf("op doesn't support debugging")
	}
	var mounts []gwclient.Mount
	for _, mnt := range exec.Mounts {
		mounts = append(mounts, gwclient.Mount{
			Selector:  mnt.Selector,
			Dest:      mnt.Dest,
			ResultID:  mountIDs[mnt.Output],
			Readonly:  mnt.Readonly,
			MountType: mnt.MountType,
			CacheOpt:  mnt.CacheOpt,
			SecretOpt: mnt.SecretOpt,
			SSHOpt:    mnt.SSHOpt,
		})
	}

	ctr, err := c.NewContainer(ctx, gwclient.NewContainerRequest{
		Mounts:      mounts,
		NetMode:     exec.Network,
		Platform:    op.Platform,
		Constraints: op.Constraints,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create debug container: %v", err)
	}

	meta := exec.Meta
	proc, err := ctr.Start(ctx, gwclient.StartRequest{
		Args:         []string{"sh"},
		Env:          meta.Env,
		User:         meta.User,
		Cwd:          meta.Cwd,
		Tty:          true,
		Stdin:        stdin,
		Stdout:       stdout,
		Stderr:       stderr,
		SecurityMode: exec.Security,
	})
	if err != nil {
		ctr.Release(ctx)
		return nil, nil, fmt.Errorf("failed to start container: %w", err)
	}
	return proc, ctr.Release, nil
}

type evaluateClient struct {
	gwclient.Client
}

func (c *evaluateClient) Solve(ctx context.Context, req gwclient.SolveRequest) (*gwclient.Result, error) {
	req.Evaluate = true
	return c.Client.Solve(ctx, req)
}
