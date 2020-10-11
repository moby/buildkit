// +build !linux

package runcexecutor

import (
	"context"
	"os/exec"

	"github.com/containerd/containerd"
	runc "github.com/containerd/go-runc"
	"github.com/moby/buildkit/executor"
	"github.com/opencontainers/runtime-spec/specs-go"
	"github.com/pkg/errors"
)

var unsupportedConsoleError = errors.New("tty for runc is only supported on linux")

func (w *runcExecutor) run(ctx context.Context, id, bundle string, process executor.ProcessInfo) (int, error) {
	if process.Meta.Tty {
		return 0, unsupportedConsoleError
	}
	return w.runc.Run(ctx, id, bundle, &runc.CreateOpts{
		IO:      &forwardIO{stdin: process.Stdin, stdout: process.Stdout, stderr: process.Stderr},
		NoPivot: w.noPivot,
	})
}

func (w *runcExecutor) exec(ctx context.Context, id, bundle string, specsProcess *specs.Process, process executor.ProcessInfo) (int, error) {
	if process.Meta.Tty {
		return 0, unsupportedConsoleError
	}
	err := w.runc.Exec(ctx, id, *specsProcess, &runc.ExecOpts{
		IO: &forwardIO{stdin: process.Stdin, stdout: process.Stdout, stderr: process.Stderr},
	})

	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return exitError.ExitCode(), err
	}
	if err != nil {
		return containerd.UnknownExitStatus, err
	}
	return 0, nil
}
