package integration

import (
	"context"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/docker/docker/client"
	"github.com/moby/buildkit/identity"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"golang.org/x/sync/errgroup"
)

const (
	shortLen      = 12
	dockerdBinary = "dockerd"
)

// InitDockerdWorker registers a dockerd worker with the global registry.
func InitDockerdWorker() {
	Register(&dockerd{})
}

type dockerd struct{}

func (c dockerd) Name() string {
	return dockerdBinary
}

func (c dockerd) Rootless() bool {
	return false
}

func (c dockerd) New(ctx context.Context, cfg *BackendConfig) (b Backend, cl func() error, err error) {
	if err := requireRoot(); err != nil {
		return nil, nil, err
	}

	deferF := &multiCloser{}
	cl = deferF.F()

	defer func() {
		if err != nil {
			deferF.F()()
			cl = nil
		}
	}()

	var proxyGroup errgroup.Group
	deferF.append(proxyGroup.Wait)

	workDir, err := os.MkdirTemp("", "integration")
	if err != nil {
		return nil, nil, err
	}

	dockerdBinaryPath, err := exec.LookPath(dockerdBinary)
	if err != nil {
		return nil, nil, errors.Wrapf(err, "could not find docker binary in $PATH")
	}

	id := "d" + identity.NewID()[:shortLen]
	dir := filepath.Join(workDir, id)
	daemonFolder, err := filepath.Abs(dir)
	if err != nil {
		return nil, nil, err
	}
	daemonRoot := filepath.Join(daemonFolder, "root")
	if err := os.MkdirAll(daemonRoot, 0755); err != nil {
		return nil, nil, errors.Wrapf(err, "failed to create daemon root %q", daemonRoot)
	}
	execRoot := filepath.Join(os.TempDir(), "dxr", id)
	daemonSocket := "unix://" + filepath.Join(daemonFolder, "docker.sock")

	cmd := exec.Command(dockerdBinaryPath, []string{
		"--data-root", daemonRoot,
		"--exec-root", execRoot,
		"--pidfile", filepath.Join(daemonFolder, "docker.pid"),
		"--host", daemonSocket,
		"--userland-proxy=false",
		"--containerd-namespace", id,
		"--containerd-plugins-namespace", id + "p",
		"--bip", "10.66.66.1/24",
		"--default-address-pool", "base=10.66.66.0/16,size=24",
		"--debug",
	}...)
	cmd.Env = append(os.Environ(), "DOCKER_SERVICE_PREFER_OFFLINE_IMAGE=1", "BUILDKIT_DEBUG_EXEC_OUTPUT=1", "BUILDKIT_DEBUG_PANIC_ON_ERROR=1")
	cmd.SysProcAttr = getSysProcAttr()

	dockerdStop, err := startCmd(cmd, cfg.Logs)
	if err != nil {
		return nil, nil, errors.Wrapf(err, "dockerd startcmd error: %s", formatLogs(cfg.Logs))
	}
	if err := waitUnix(daemonSocket, 15*time.Second); err != nil {
		dockerdStop()
		return nil, nil, errors.Wrapf(err, "dockerd did not start up: %s", formatLogs(cfg.Logs))
	}
	deferF.append(dockerdStop)

	ctx, cancel := context.WithCancel(context.Background())
	deferF.append(func() error { cancel(); return nil })

	dockerAPI, err := client.NewClientWithOpts(
		client.FromEnv,
		client.WithHost(daemonSocket),
	)
	if err != nil {
		return nil, nil, errors.Wrapf(err, "dockerd client api error: %s", formatLogs(cfg.Logs))
	}
	deferF.append(dockerAPI.Close)

	err = waitForAPI(ctx, dockerAPI, 5*time.Second)
	if err != nil {
		return nil, nil, errors.Wrapf(err, "dockerd client api timed out: %s", formatLogs(cfg.Logs))
	}

	// Create a file descriptor to be used as a Unix domain socket.
	// Remove it immediately (the name will still be valid for the socket) so that
	// we don't leave files all over the users tmp tree.
	f, err := os.CreateTemp("", "buildkit-integration")
	if err != nil {
		return
	}
	localPath := f.Name()
	f.Close()
	os.Remove(localPath)

	listener, err := net.Listen("unix", localPath)
	if err != nil {
		return nil, nil, errors.Wrapf(err, "dockerd listener error: %s", formatLogs(cfg.Logs))
	}
	deferF.append(listener.Close)

	proxyGroup.Go(func() error {
		for {
			tmpConn, err := listener.Accept()
			if err != nil {
				// Ignore the error from accept which is always a system error.
				return nil
			}
			conn, err := dockerAPI.DialHijack(ctx, "/grpc", "h2c", nil)
			if err != nil {
				if errors.Is(err, syscall.ECONNRESET) || errors.Is(err, net.ErrClosed) {
					logrus.Warn("dockerd conn already closed: ", err)
					return nil
				}
				return errors.Wrap(err, "dockerd grpc conn error")
			}

			proxyGroup.Go(func() error {
				_, err := io.Copy(conn, tmpConn)
				if err != nil {
					logrus.Warn("dockerd proxy error: ", err)
					return nil
				}
				return tmpConn.Close()
			})
			proxyGroup.Go(func() error {
				_, err := io.Copy(tmpConn, conn)
				if err != nil {
					logrus.Warn("dockerd proxy error: ", err)
					return nil
				}
				return conn.Close()
			})
		}
	})

	return backend{
		address:   "unix://" + listener.Addr().String(),
		rootless:  false,
		isDockerd: true,
	}, cl, nil
}

func waitForAPI(ctx context.Context, apiClient *client.Client, d time.Duration) error {
	step := 50 * time.Millisecond
	i := 0
	for {
		if _, err := apiClient.Ping(ctx); err == nil {
			break
		}
		i++
		if time.Duration(i)*step > d {
			return errors.New("failed to connect to /_ping endpoint")
		}
		time.Sleep(step)
	}
	return nil
}

func SkipIfDockerd(t *testing.T, sb Sandbox, reason ...string) {
	t.Helper()
	sbx, ok := sb.(*sandbox)
	if !ok {
		t.Fatalf("invalid sandbox type %T", sb)
	}
	b, ok := sbx.Backend.(backend)
	if !ok {
		t.Fatalf("invalid backend type %T", b)
	}
	if b.isDockerd {
		t.Skipf("dockerd worker can not currently run this test due to missing features (%s)", strings.Join(reason, ", "))
	}
}

func IsTestDockerd() bool {
	return os.Getenv("TEST_DOCKERD") == "1"
}
