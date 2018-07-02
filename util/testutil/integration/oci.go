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
	register(&oci{})
}

type oci struct {
}

func (s *oci) Name() string {
	return "oci"
}

func (s *oci) New() (Sandbox, func() error, error) {
	if err := lookupBinary("buildkitd"); err != nil {
		return nil, nil, err
	}
	if err := requireRoot(); err != nil {
		return nil, nil, err
	}
	logs := map[string]*bytes.Buffer{}
	buildkitdSock, stop, err := runBuildkitd([]string{"buildkitd", "--oci-worker=true", "--containerd-worker=false"}, logs)
	if err != nil {
		return nil, nil, err
	}

	deferF := &multiCloser{}
	deferF.append(stop)

	return &sandbox{address: buildkitdSock, logs: logs, cleanup: deferF}, deferF.F(), nil
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

func runBuildkitd(args []string, logs map[string]*bytes.Buffer) (address string, cl func() error, err error) {
	deferF := &multiCloser{}
	cl = deferF.F()

	defer func() {
		if err != nil {
			deferF.F()()
			cl = nil
		}
	}()

	tmpdir, err := ioutil.TempDir("", "bktest_buildkitd")
	if err != nil {
		return "", nil, err
	}
	deferF.append(func() error { return os.RemoveAll(tmpdir) })

	address = "unix://" + filepath.Join(tmpdir, "buildkitd.sock")
	if runtime.GOOS == "windows" {
		address = "//./pipe/buildkitd-" + filepath.Base(tmpdir)
	}

	args = append(args, "--root", tmpdir, "--addr", address, "--debug")
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = append(os.Environ(), "BUILDKIT_DEBUG_EXEC_OUTPUT=1")

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
