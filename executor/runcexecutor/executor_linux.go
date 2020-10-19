package runcexecutor

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/containerd/console"
	runc "github.com/containerd/go-runc"
	"github.com/docker/docker/pkg/signal"
	"github.com/moby/buildkit/executor"
	"github.com/opencontainers/runtime-spec/specs-go"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"golang.org/x/sync/errgroup"
)

func updateRuncFieldsForHostOS(runtime *runc.Runc) {
	// PdeathSignal only supported on unix platforms
	runtime.PdeathSignal = syscall.SIGKILL // this can still leak the process
}

func (w *runcExecutor) run(ctx context.Context, id, bundle string, process executor.ProcessInfo) error {
	return w.callWithIO(ctx, id, bundle, process, func(ctx context.Context, pidfile string, io runc.IO) error {
		_, err := w.runc.Run(ctx, id, bundle, &runc.CreateOpts{
			NoPivot: w.noPivot,
			PidFile: pidfile,
			IO:      io,
		})
		return err
	})
}

func (w *runcExecutor) exec(ctx context.Context, id, bundle string, specsProcess *specs.Process, process executor.ProcessInfo) error {
	return w.callWithIO(ctx, id, bundle, process, func(ctx context.Context, pidfile string, io runc.IO) error {
		return w.runc.Exec(ctx, id, *specsProcess, &runc.ExecOpts{
			PidFile: pidfile,
			IO:      io,
		})
	})
}

type runcCall func(ctx context.Context, pidfile string, io runc.IO) error

func (w *runcExecutor) callWithIO(ctx context.Context, id, bundle string, process executor.ProcessInfo, call runcCall) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	pidfile, err := ioutil.TempFile(bundle, "*.pid")
	if err != nil {
		return errors.Wrap(err, "failed to create pidfile")
	}
	defer os.Remove(pidfile.Name())
	pidfile.Close()

	if !process.Meta.Tty {
		return call(ctx, pidfile.Name(), &forwardIO{stdin: process.Stdin, stdout: process.Stdout, stderr: process.Stderr})
	}

	ptm, ptsName, err := console.NewPty()
	if err != nil {
		return err
	}

	pts, err := os.OpenFile(ptsName, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		ptm.Close()
		return err
	}

	eg, ctx := errgroup.WithContext(ctx)

	defer func() {
		if process.Stdin != nil {
			process.Stdin.Close()
		}
		pts.Close()
		ptm.Close()
		cancel() // this will shutdown resize loop
		err := eg.Wait()
		if err != nil {
			logrus.Warningf("error while shutting down tty io: %s", err)
		}
	}()

	if process.Stdin != nil {
		eg.Go(func() error {
			_, err := io.Copy(ptm, process.Stdin)
			// stdin might be a pipe, so this is like EOF
			if errors.Is(err, io.ErrClosedPipe) {
				return nil
			}
			return err
		})
	}

	if process.Stdout != nil {
		eg.Go(func() error {
			_, err := io.Copy(process.Stdout, ptm)
			// ignore `read /dev/ptmx: input/output error` when ptm is closed
			var ptmClosedError *os.PathError
			if errors.As(err, &ptmClosedError) {
				if ptmClosedError.Op == "read" &&
					ptmClosedError.Path == "/dev/ptmx" &&
					ptmClosedError.Err == syscall.EIO {
					return nil
				}
			}
			return err
		})
	}

	eg.Go(func() error {
		// need to poll until the pidfile has the pid written to it
		pidfileCtx, timeout := context.WithTimeout(ctx, 10*time.Second)
		defer timeout()

		var runcProcess *os.Process
		for {
			st, err := os.Stat(pidfile.Name())
			if err == nil && st.Size() > 0 {
				pid, err := runc.ReadPidFile(pidfile.Name())
				if err != nil {
					return errors.Wrapf(err, "unable to read pid file: %s", pidfile.Name())
				}
				// pid will be for the process in process.Meta, not the parent runc process.
				// We need to send SIGWINCH to the runc process, not the process.Meta process.
				ppid, err := getppid(pid)
				if err != nil {
					return errors.Wrapf(err, "unable to find runc process (parent of %d)", pid)
				}
				runcProcess, err = os.FindProcess(ppid)
				if err != nil {
					return errors.Wrapf(err, "unable to find process for pid %d", ppid)
				}
				break
			}
			select {
			case <-pidfileCtx.Done():
				return errors.New("pidfile never updated")
			case <-time.After(100 * time.Microsecond):
			}
		}
		for {
			select {
			case <-ctx.Done():
				return nil
			case resize := <-process.Resize:
				err = ptm.Resize(console.WinSize{
					Height: uint16(resize.Rows),
					Width:  uint16(resize.Cols),
				})
				if err != nil {
					logrus.Errorf("failed to resize ptm: %s", err)
				}
				err = runcProcess.Signal(signal.SIGWINCH)
				if err != nil {
					logrus.Errorf("failed to send SIGWINCH to process: %s", err)
				}
			}
		}
	})

	runcIO := &forwardIO{}
	if process.Stdin != nil {
		runcIO.stdin = pts
	}
	if process.Stdout != nil {
		runcIO.stdout = pts
	}
	if process.Stderr != nil {
		runcIO.stderr = pts
	}

	return call(ctx, pidfile.Name(), runcIO)
}

const PPidStatusPrefix = "PPid:\t"

func getppid(pid int) (int, error) {
	fh, err := os.Open(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return -1, err
	}

	defer fh.Close()
	scanner := bufio.NewScanner(fh)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, PPidStatusPrefix) {
			return strconv.Atoi(strings.TrimPrefix(line, PPidStatusPrefix))
		}
	}
	return -1, errors.Errorf("PPid line not found in /proc/%d/status", pid)
}
