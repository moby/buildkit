package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	sddaemon "github.com/coreos/go-systemd/daemon"
	"github.com/google/shlex"
	"github.com/moby/buildkit/util/appdefaults"
	"github.com/moby/buildkit/util/sysprocattr"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

type spawn struct {
	flags           []string
	buildkitdSocket string
}

func NewSpawn(flagsStr string) (*spawn, error) {
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
	dl := &spawn{
		flags:           flags,
		buildkitdSocket: buildkitdSocket,
	}
	return dl, nil
}

// GetAddress returns a address of a daemon.
// If no instance is running, a daemon is spawned.
func (dl *spawn) GetAddress(timeout time.Duration) (string, error) {
	attempt, err := net.Dial("unix", dl.buildkitdSocket)
	if err == nil {
		logrus.Debugf("spawn: no need to spawn")
		attempt.Close()
		buildkitdAddr := "unix://" + dl.buildkitdSocket
		return buildkitdAddr, nil
	}
	return dl.spawn(timeout)
}

func (dl *spawn) spawn(timeout time.Duration) (string, error) {
	buildkitdAddr := "unix://" + dl.buildkitdSocket
	const (
		buildkitd   = "buildkitd"
		persistence = 10 // daemon automatically exits when idle for persistence seconds, as in wineserver
		rootlesskit = "rootlesskit"
	)
	flags := append(dl.flags, "--addr="+buildkitdAddr, fmt.Sprintf("--persistence=%d", persistence))
	dir := filepath.Dir(dl.buildkitdSocket)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	notifySocket := filepath.Join(dir, ".dl-notify.sock")
	os.RemoveAll(notifySocket)
	laddr := net.UnixAddr{Name: notifySocket, Net: "unixgram"}
	notifyConn, err := net.ListenUnixgram("unixgram", &laddr)
	if err != nil {
		return "", err
	}
	defer notifyConn.Close()
	defer os.RemoveAll(notifySocket)
	var cmd *exec.Cmd
	if os.Geteuid() == 0 {
		cmd = exec.Command(buildkitd, flags...)
	} else {
		cmd = exec.Command(rootlesskit, append([]string{buildkitd}, flags...)...)
	}
	cmd.Env = append(os.Environ(), "NOTIFY_SOCKET="+notifySocket)
	cmd.Stdout = &logrusWriter{debugPrefix: "spawn(stdout): "}
	cmd.Stderr = &logrusWriter{debugPrefix: "spawn(stderr): "}
	sysprocattr.Setsid(cmd, true)
	logrus.Debugf("spawn: spawning %q %v in background", cmd.Path, cmd.Args)
	if err := cmd.Start(); err != nil {
		return "", err
	}
	cmd.Process.Release()
	go func() {
		werr := cmd.Wait()
		if _, ok := werr.(*exec.ExitError); ok {
			panic(errors.Wrapf(werr, "%q %v failed, try --debug to see the logs", cmd.Path, cmd.Args))
		}
	}()
	if err := waitForSdNotifyReady(notifyConn, timeout); err != nil {
		return "", err
	}
	return buildkitdAddr, nil
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
