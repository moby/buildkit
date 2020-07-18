package integration

import (
	"bufio"
	"bytes"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/google/shlex"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

const buildkitdConfigFile = "buildkitd.toml"

type backend struct {
	address           string
	containerdAddress string
	rootless          bool
}

func (b backend) Address() string {
	return b.address
}

func (b backend) ContainerdAddress() string {
	return b.containerdAddress
}

func (b backend) Rootless() bool {
	return b.rootless
}

type sandbox struct {
	Backend

	logs    map[string]*bytes.Buffer
	cleanup *multiCloser
	mv      matrixValue
}

func (sb *sandbox) PrintLogs(t *testing.T) {
	printLogs(sb.logs, t.Log)
}

func (sb *sandbox) NewRegistry() (string, error) {
	url, cl, err := NewRegistry("")
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

func (sb *sandbox) Value(k string) interface{} {
	return sb.mv.values[k].value
}

func newSandbox(w Worker, mirror string, mv matrixValue) (s Sandbox, cl func() error, err error) {
	cfg := &BackendConfig{
		Logs: make(map[string]*bytes.Buffer),
	}

	var upt []ConfigUpdater
	for _, v := range mv.values {
		if u, ok := v.value.(ConfigUpdater); ok {
			upt = append(upt, u)
		}
	}

	if mirror != "" {
		upt = append(upt, withMirrorConfig(mirror))
	}

	deferF := &multiCloser{}
	cl = deferF.F()

	defer func() {
		if err != nil {
			deferF.F()()
			cl = nil
		}
	}()

	if len(upt) > 0 {
		dir, err := writeConfig(upt)
		if err != nil {
			return nil, nil, err
		}
		deferF.append(func() error {
			return os.RemoveAll(dir)
		})
		cfg.ConfigFile = filepath.Join(dir, buildkitdConfigFile)
	}

	b, closer, err := w.New(cfg)
	if err != nil {
		return nil, nil, err
	}
	deferF.append(closer)

	return &sandbox{
		Backend: b,
		logs:    cfg.Logs,
		cleanup: deferF,
		mv:      mv,
	}, cl, nil
}

func getBuildkitdAddr(tmpdir string) string {
	address := "unix://" + filepath.Join(tmpdir, "buildkitd.sock")
	if runtime.GOOS == "windows" {
		address = "//./pipe/buildkitd-" + filepath.Base(tmpdir)
	}
	return address
}

func runBuildkitd(conf *BackendConfig, args []string, logs map[string]*bytes.Buffer, uid, gid int) (address string, cl func() error, err error) {
	deferF := &multiCloser{}
	cl = deferF.F()

	defer func() {
		if err != nil {
			deferF.F()()
			cl = nil
		}
	}()

	if conf.ConfigFile != "" {
		args = append(args, "--config="+conf.ConfigFile)
	}

	tmpdir, err := ioutil.TempDir("", "bktest_buildkitd")
	if err != nil {
		return "", nil, err
	}
	if err := os.Chown(tmpdir, uid, gid); err != nil {
		return "", nil, err
	}
	if err := os.MkdirAll(filepath.Join(tmpdir, "tmp"), 0711); err != nil {
		return "", nil, err
	}
	if err := os.Chown(filepath.Join(tmpdir, "tmp"), uid, gid); err != nil {
		return "", nil, err
	}

	deferF.append(func() error { return os.RemoveAll(tmpdir) })

	address = getBuildkitdAddr(tmpdir)

	args = append(args, "--root", tmpdir, "--addr", address, "--debug")
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = append(os.Environ(), "BUILDKIT_DEBUG_EXEC_OUTPUT=1", "BUILDKIT_DEBUG_PANIC_ON_ERROR=1", "TMPDIR="+filepath.Join(tmpdir, "tmp"))
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid: true, // stretch sudo needs this for sigterm
	}

	if stop, err := startCmd(cmd, logs); err != nil {
		return "", nil, err
	} else {
		deferF.append(stop)
	}

	if err := waitUnix(address, 15*time.Second); err != nil {
		return "", nil, err
	}

	deferF.append(func() error {
		f, err := os.Open("/proc/self/mountinfo")
		if err != nil {
			return errors.Wrap(err, "failed to open mountinfo")
		}
		defer f.Close()
		s := bufio.NewScanner(f)
		for s.Scan() {
			if strings.Contains(s.Text(), tmpdir) {
				return errors.Errorf("leaked mountpoint for %s", tmpdir)
			}
		}
		return s.Err()
	})

	return address, cl, err
}

func rootlessSupported(uid int) bool {
	cmd := exec.Command("sudo", "-u", fmt.Sprintf("#%d", uid), "-i", "--", "unshare", "-U", "true")
	b, err := cmd.CombinedOutput()
	if err != nil {
		logrus.Warnf("rootless mode is not supported on this host: %v (%s)", err, string(b))
		return false
	}
	return true
}

func printLogs(logs map[string]*bytes.Buffer, f func(args ...interface{})) {
	for name, l := range logs {
		f(name)
		s := bufio.NewScanner(l)
		for s.Scan() {
			f(s.Text())
		}
	}
}
