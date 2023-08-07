package integration

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/shlex"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/util/bklog"
)

const buildkitdConfigFile = "buildkitd.toml"

type sandbox struct {
	Backend

	id      string
	name    string
	ctx     context.Context
	home    string
	logs    map[string]*bytes.Buffer
	cleanup *MultiCloser
	mv      matrixValue
}

func (sb *sandbox) ID() string {
	return sb.id
}

func (sb *sandbox) Name() string {
	return sb.name
}

func (sb *sandbox) Context() context.Context {
	return sb.ctx
}

func (sb *sandbox) Home() string {
	return sb.home
}

func (sb *sandbox) Logs() map[string]*bytes.Buffer {
	return sb.logs
}

func (sb *sandbox) PrintLogs(t *testing.T) {
	PrintLogs(sb.logs, t.Log)
}

func (sb *sandbox) ClearLogs() {
	sb.logs = make(map[string]*bytes.Buffer)
}

func (sb *sandbox) NewRegistry() (string, error) {
	url, cl, err := NewRegistry("")
	if err != nil {
		return "", err
	}
	sb.cleanup.Append(cl)
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

func newSandbox(ctx context.Context, w Worker, mirror string, mv matrixValue) (s Sandbox, cl func() error, err error) {
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

	deferF := &MultiCloser{}
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
		deferF.Append(func() error {
			return os.RemoveAll(dir)
		})
		cfg.ConfigFile = filepath.Join(dir, buildkitdConfigFile)
	}

	b, closer, err := w.New(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}
	deferF.Append(closer)

	home, err := os.CreateTemp("", "buildkit-integration-home")
	if err != nil {
		return nil, nil, err
	}
	if err = home.Close(); err != nil {
		return nil, nil, err
	}
	deferF.append(func() error {
		return os.RemoveAll(home.Name())
	})

	return &sandbox{
		Backend: b,
		id:      identity.NewID(),
		name:    w.Name(),
		ctx:     ctx,
		home:    home.Name(),
		logs:    cfg.Logs,
		cleanup: deferF,
		mv:      mv,
	}, cl, nil
}

func RootlessSupported(uid int) bool {
	cmd := exec.Command("sudo", "-u", fmt.Sprintf("#%d", uid), "-i", "--", "exec", "unshare", "-U", "true") //nolint:gosec // test utility
	b, err := cmd.CombinedOutput()
	if err != nil {
		bklog.L.Warnf("rootless mode is not supported on this host: %v (%s)", err, string(b))
		return false
	}
	return true
}

func PrintLogs(logs map[string]*bytes.Buffer, f func(args ...interface{})) {
	for name, l := range logs {
		f(name)
		s := bufio.NewScanner(l)
		for s.Scan() {
			f(s.Text())
		}
	}
}

func FormatLogs(m map[string]*bytes.Buffer) string {
	var ss []string
	for k, b := range m {
		if b != nil {
			ss = append(ss, fmt.Sprintf("%q:%q", k, b.String()))
		}
	}
	return strings.Join(ss, ",")
}

func CheckFeatureCompat(t *testing.T, sb Sandbox, features map[string]struct{}, reason ...string) {
	t.Helper()
	if len(reason) == 0 {
		t.Fatal("no reason provided")
	}
	var ereasons []string
	for _, r := range reason {
		if _, ok := features[r]; ok {
			if !sb.Supports(r) {
				ereasons = append(ereasons, r)
			}
		} else {
			sb.ClearLogs()
			t.Fatalf("unknown reason %q to skip test", r)
		}
	}
	if len(ereasons) > 0 {
		t.Skipf("%s worker can not currently run this test due to missing features (%s)", sb.Name(), strings.Join(ereasons, ", "))
	}
}
