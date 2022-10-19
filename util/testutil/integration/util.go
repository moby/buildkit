package integration

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/containerd/continuity/fs/fstest"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

func startCmd(cmd *exec.Cmd, logs map[string]*bytes.Buffer) (func() error, error) {
	if logs != nil {
		b := new(bytes.Buffer)
		logs["stdout: "+cmd.Path] = b
		cmd.Stdout = &lockingWriter{Writer: b}
		b = new(bytes.Buffer)
		logs["stderr: "+cmd.Path] = b
		cmd.Stderr = &lockingWriter{Writer: b}
	}

	fmt.Fprintf(cmd.Stderr, "> startCmd %v %+v\n", time.Now(), cmd.Args)

	if err := cmd.Start(); err != nil {
		return nil, err
	}
	eg, ctx := errgroup.WithContext(context.TODO())

	stopped := make(chan struct{})
	stop := make(chan struct{})
	eg.Go(func() error {
		st, err := cmd.Process.Wait()
		fmt.Fprintf(cmd.Stderr, "> stopped %v %+v %v\n", time.Now(), st, st.ExitCode())
		close(stopped)
		select {
		case <-stop:
			return nil
		default:
			return err
		}
	})

	eg.Go(func() error {
		select {
		case <-ctx.Done():
		case <-stopped:
		case <-stop:
			fmt.Fprintf(cmd.Stderr, "> sending sigterm %v\n", time.Now())
			cmd.Process.Signal(syscall.SIGTERM)
			go func() {
				select {
				case <-stopped:
				case <-time.After(20 * time.Second):
					cmd.Process.Kill()
				}
			}()
		}
		return nil
	})

	return func() error {
		close(stop)
		return eg.Wait()
	}, nil
}

func waitUnix(address string, d time.Duration) error {
	address = strings.TrimPrefix(address, "unix://")
	addr, err := net.ResolveUnixAddr("unix", address)
	if err != nil {
		return errors.Wrapf(err, "failed resolving unix addr: %s", address)
	}

	step := 50 * time.Millisecond
	i := 0
	for {
		if conn, err := net.DialUnix("unix", nil, addr); err == nil {
			conn.Close()
			break
		}
		i++
		if time.Duration(i)*step > d {
			return errors.Errorf("failed dialing: %s", address)
		}
		time.Sleep(step)
	}
	return nil
}

type multiCloser struct {
	fns []func() error
}

func (mc *multiCloser) F() func() error {
	return func() error {
		var err error
		for i := range mc.fns {
			if err1 := mc.fns[len(mc.fns)-1-i](); err == nil {
				err = err1
			}
		}
		mc.fns = nil
		return err
	}
}

func (mc *multiCloser) append(f func() error) {
	mc.fns = append(mc.fns, f)
}

var ErrRequirements = errors.Errorf("missing requirements")

func lookupBinary(name string) error {
	_, err := exec.LookPath(name)
	if err != nil {
		return errors.Wrapf(ErrRequirements, "failed to lookup %s binary", name)
	}
	return nil
}

func requireRoot() error {
	if os.Getuid() != 0 {
		return errors.Wrap(ErrRequirements, "requires root")
	}
	return nil
}

type lockingWriter struct {
	mu sync.Mutex
	io.Writer
}

func (w *lockingWriter) Write(dt []byte) (int, error) {
	w.mu.Lock()
	n, err := w.Writer.Write(dt)
	w.mu.Unlock()
	return n, err
}

func Tmpdir(t *testing.T, appliers ...fstest.Applier) (string, error) {
	// We cannot use t.TempDir() to create a temporary directory here because
	// appliers might contain fstest.CreateSocket. If the test name is too long,
	// t.TempDir() could return a path that is longer than 108 characters. This
	// would result in "bind: invalid argument" when we listen on the socket.
	tmpdir, err := os.MkdirTemp("", "buildkit")
	if err != nil {
		return "", err
	}

	t.Cleanup(func() {
		require.NoError(t, os.RemoveAll(tmpdir))
	})

	if err := fstest.Apply(appliers...).Apply(tmpdir); err != nil {
		return "", err
	}
	return tmpdir, nil
}
