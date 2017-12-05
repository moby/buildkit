package integration

import (
	"bufio"
	"bytes"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/google/shlex"
)

func init() {
	register(&standalone{})
}

type standalone struct {
}

func (s *standalone) Name() string {
	return "standalone"
}

func (s *standalone) New() (Sandbox, func() error, error) {
	if err := lookupBinary("buildd-standalone"); err != nil {
		return nil, nil, err
	}
	if err := requireRoot(); err != nil {
		return nil, nil, err
	}
	logs := map[string]*bytes.Buffer{}
	builddSock, stop, err := runBuildd([]string{"buildd-standalone"}, logs)
	if err != nil {
		return nil, nil, err
	}

	deferF := &multiCloser{}
	deferF.append(stop)

	return &sandbox{address: builddSock, logs: logs, cleanup: deferF}, deferF.F(), nil
}

type sandbox struct {
	address string
	logs    map[string]*bytes.Buffer
	cleanup *multiCloser
}

func (sb *sandbox) Address() string {
	return sb.address
}

func (sb *sandbox) PrintLogs(t *testing.T) {
	for name, l := range sb.logs {
		t.Log(name)
		s := bufio.NewScanner(l)
		for s.Scan() {
			t.Log(s.Text())
		}
	}
}

func (sb *sandbox) NewRegistry() (string, error) {
	url, cl, err := newRegistry()
	if err != nil {
		return "", err
	}
	sb.cleanup.append(cl)
	return url, nil
}

func (sb *sandbox) Cmd(args ...string) *exec.Cmd {
	if len(args) == 1 {
		if split, err := shlex.Split(args[0]); err == nil {
			args = split
		}
	}
	cmd := exec.Command("buildctl", args...)
	cmd.Env = append(cmd.Env, os.Environ()...)
	cmd.Env = append(cmd.Env, "BUILDKIT_HOST="+sb.Address())
	return cmd
}

func runBuildd(args []string, logs map[string]*bytes.Buffer) (address string, cl func() error, err error) {
	deferF := &multiCloser{}
	cl = deferF.F()

	defer func() {
		if err != nil {
			deferF.F()()
			cl = nil
		}
	}()

	tmpdir, err := ioutil.TempDir("", "bktest_buildd")
	if err != nil {
		return "", nil, err
	}
	deferF.append(func() error { return os.RemoveAll(tmpdir) })

	address = "unix://" + filepath.Join(tmpdir, "buildd.sock")
	if runtime.GOOS == "windows" {
		address = "//./pipe/buildd-" + filepath.Base(tmpdir)
	}

	args = append(args, "--root", tmpdir, "--addr", address, "--debug")
	cmd := exec.Command(args[0], args[1:]...)

	if stop, err := startCmd(cmd, logs); err != nil {
		return "", nil, err
	} else {
		deferF.append(stop)
	}

	if err := waitUnix(address, 5*time.Second); err != nil {
		return "", nil, err
	}

	return
}
