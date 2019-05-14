package spawner

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	sddaemon "github.com/coreos/go-systemd/daemon"
	"github.com/google/shlex"
	"github.com/moby/buildkit/util/appdefaults"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"golang.org/x/sync/errgroup"
)

type spawner struct {
	flags           []string
	buildkitdSocket string
	closer          func() error
}

func New(flagsStr string) (Spawner, error) {
	flags, err := shlex.Split(flagsStr)
	if err != nil {
		return nil, err
	}
	buildkitdAddr := appdefaults.Address
	if os.Geteuid() != 0 {
		buildkitdAddr = appdefaults.UserAddress()
	}
	if !strings.HasPrefix(buildkitdAddr, "unix://") {
		return nil, errors.Errorf("unexpected non-unix address %q", buildkitdAddr)
	}
	buildkitdSocket := strings.TrimPrefix(buildkitdAddr, "unix://")
	sp := &spawner{
		flags:           flags,
		buildkitdSocket: buildkitdSocket,
	}
	return sp, nil
}

func (sp *spawner) cmd(buildkitdAddr, notifySocket string) *exec.Cmd {
	const (
		buildkitd   = "buildkitd"
		rootlesskit = "rootlesskit"
	)
	flags := append(sp.flags, "--addr="+buildkitdAddr)
	var cmd *exec.Cmd
	if os.Geteuid() == 0 {
		cmd = exec.Command(buildkitd, flags...)
	} else {
		cmd = exec.Command(rootlesskit, append([]string{buildkitd}, flags...)...)
	}
	cmd.Env = append(os.Environ(), "NOTIFY_SOCKET="+notifySocket)
	cmd.Stdout = &logrusWriter{debugPrefix: "spawn(stdout): "}
	cmd.Stderr = &logrusWriter{debugPrefix: "spawn(stderr): "}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid:   true,
		Pdeathsig: syscall.SIGTERM,
	}
	return cmd
}

func (sp *spawner) Spawn(timeout time.Duration) (string, error) {
	buildkitdAddr := "unix://" + sp.buildkitdSocket
	dir := filepath.Dir(sp.buildkitdSocket)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	notifySocket := filepath.Join(dir, ".spawn-notify.sock")
	os.RemoveAll(notifySocket)
	laddr := net.UnixAddr{Name: notifySocket, Net: "unixgram"}
	notifyConn, err := net.ListenUnixgram("unixgram", &laddr)
	if err != nil {
		os.RemoveAll(notifySocket)
		return "", err
	}
	defer notifyConn.Close()
	cmd := sp.cmd(buildkitdAddr, notifySocket)
	logrus.Debugf("spawning %q %v in background", cmd.Path, cmd.Args)
	if err := cmd.Start(); err != nil {
		os.RemoveAll(notifySocket)
		return "", err
	}
	eg, ctx := errgroup.WithContext(context.TODO())
	stopped := make(chan struct{})
	stop := make(chan struct{})
	eg.Go(func() error {
		_, err := cmd.Process.Wait()
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
			pgid, err := syscall.Getpgid(cmd.Process.Pid)
			if err != nil {
				return err
			}
			syscall.Kill(-pgid, syscall.SIGTERM)
			select {
			case <-stopped:
			case <-time.After(10 * time.Second):
				return syscall.Kill(-pgid, syscall.SIGKILL)
			}
		}
		return nil
	})
	sp.closer = func() error {
		os.RemoveAll(notifySocket)
		close(stop)
		return eg.Wait()
	}
	readyCh := make(chan error)
	go func() {
		readyCh <- waitForSdNotifyReady(notifyConn, timeout)
	}()
	select {
	case err := <-readyCh:
		if err != nil {
			return "", err
		}
	case <-stopped:
		return "", errors.New("buildkitd exited, try --debug to see the buildkitd logs")
	}
	return buildkitdAddr, nil
}

func (sp *spawner) Close() error {
	if sp.closer != nil {
		err := sp.closer()
		sp.closer = nil
		return err
	}
	return nil
}

func waitForSdNotifyReady(c net.Conn, timeout time.Duration) error {
	b := make([]byte, 1024)
	deadline := time.Now().Add(timeout)
	c.SetDeadline(deadline)
	for {
		n, err := c.Read(b)
		if err != nil {
			if nerr, ok := err.(net.Error); ok {
				if nerr.Timeout() {
					break
				}
			}
			return err
		}
		kvs := strings.Split(string(b[:n]), "\n")
		for _, kv := range kvs {
			kv = strings.TrimSpace(kv)
			if kv == sddaemon.SdNotifyReady {
				return nil
			}
		}
	}
	return errors.Errorf("could not get %s notification from spawned buildkitd in %v, try --debug to see the buildkitd logs", sddaemon.SdNotifyReady, timeout)
}

type logrusWriter struct {
	debugPrefix string
}

func (w *logrusWriter) Write(p []byte) (int, error) {
	logrus.Debugf("%s%s", w.debugPrefix, string(p))
	return len(p), nil
}
