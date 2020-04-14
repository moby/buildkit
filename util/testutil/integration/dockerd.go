package integration

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"os"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/docker/docker/testutil/daemon"
)

const dockerdBinary = "dockerd"

type logTAdapter struct {
	Name string
	Logs map[string]*bytes.Buffer
}

func (l logTAdapter) Logf(format string, v ...interface{}) {
	if buf, ok := l.Logs[l.Name]; !ok || buf == nil {
		l.Logs[l.Name] = &bytes.Buffer{}
	}
	fmt.Fprintf(l.Logs[l.Name], format, v...)
}

// InitDockerdWorker registers a dockerd worker with the global registry.
func InitDockerdWorker() {
	Register(&dockerd{})
}

type dockerd struct{}

func (c dockerd) Name() string {
	return dockerdBinary
}

func (c dockerd) New(cfg *BackendConfig) (b Backend, cl func() error, err error) {
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

	workDir, err := ioutil.TempDir("", "integration")
	if err != nil {
		return nil, nil, err
	}

	cmd, err := daemon.NewDaemon(
		workDir,
		daemon.WithTestLogger(logTAdapter{
			Name: "creatingDaemon",
			Logs: cfg.Logs,
		}),
		daemon.WithContainerdSocket(""),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("new daemon error: %q, %s", err, formatLogs(cfg.Logs))
	}

	err = cmd.StartWithError()
	if err != nil {
		return nil, nil, err
	}
	deferF.append(cmd.StopWithError)

	logs := map[string]*bytes.Buffer{}
	if err := waitUnix(cmd.Sock(), 5*time.Second); err != nil {
		return nil, nil, fmt.Errorf("dockerd did not start up: %q, %s", err, formatLogs(logs))
	}

	ctx, cancel := context.WithCancel(context.Background())
	deferF.append(func() error { cancel(); return nil })

	dockerAPI, err := cmd.NewClient()
	if err != nil {
		return nil, nil, err
	}
	deferF.append(dockerAPI.Close)

	// Create a file descriptor to be used as a Unix domain socket.
	// Remove it immediately (the name will still be valid for the socket) so that
	// we don't leave files all over the users tmp tree.
	f, err := ioutil.TempFile("", "buildkit-integration")
	if err != nil {
		return
	}
	localPath := f.Name()
	f.Close()
	os.Remove(localPath)

	listener, err := net.Listen("unix", localPath)
	if err != nil {
		return nil, nil, err
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
				return err
			}

			proxyGroup.Go(func() error {
				_, err := io.Copy(conn, tmpConn)
				if err != nil {
					return err
				}
				return tmpConn.Close()
			})
			proxyGroup.Go(func() error {
				_, err := io.Copy(tmpConn, conn)
				if err != nil {
					return err
				}
				return conn.Close()
			})
		}
		cancel()
		return nil
	})

	return backend{
		address:  "unix://" + listener.Addr().String(),
		rootless: false,
	}, cl, nil
}
