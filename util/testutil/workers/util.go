package workers

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/pkg/errors"
)

func requireRoot() error {
	if os.Getuid() != 0 {
		return errors.Wrap(integration.ErrRequirements, "requires root")
	}
	return nil
}

func runBuildkitd(ctx context.Context, conf *integration.BackendConfig, args []string, logs map[string]*bytes.Buffer, uid, gid int, extraEnv []string) (address string, cl func() error, err error) {
	deferF := &integration.MultiCloser{}
	cl = deferF.F()

	defer func() {
		if err != nil {
			deferF.F()()
			cl = nil
		}
	}()

	tmpdir, err := os.MkdirTemp("", "bktest_buildkitd")
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
	deferF.Append(func() error { return os.RemoveAll(tmpdir) })

	cfgfile, err := integration.WriteConfig(append(conf.DaemonConfig, withOTELSocketPath(getTraceSocketPath(tmpdir))))
	if err != nil {
		return "", nil, err
	}
	deferF.Append(func() error {
		return os.RemoveAll(filepath.Dir(cfgfile))
	})
	args = append(args, "--config="+cfgfile)

	address = getBuildkitdAddr(tmpdir)

	args = append(args, "--root", tmpdir, "--addr", address, "--debug")
	cmd := exec.Command(args[0], args[1:]...) //nolint:gosec // test utility
	cmd.Env = append(os.Environ(), "BUILDKIT_DEBUG_EXEC_OUTPUT=1", "BUILDKIT_DEBUG_PANIC_ON_ERROR=1", "TMPDIR="+filepath.Join(tmpdir, "tmp"))
	cmd.Env = append(cmd.Env, extraEnv...)
	cmd.SysProcAttr = getSysProcAttr()

	stop, err := integration.StartCmd(cmd, logs)
	if err != nil {
		return "", nil, err
	}
	deferF.Append(stop)

	if err := integration.WaitUnix(address, 15*time.Second, cmd); err != nil {
		return "", nil, err
	}

	deferF.Append(func() error {
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

func withOTELSocketPath(socketPath string) integration.ConfigUpdater {
	return otelSocketPath(socketPath)
}

type otelSocketPath string

func (osp otelSocketPath) UpdateConfigFile(in string) string {
	return fmt.Sprintf(`%s

[otel]
  socketPath = %q
`, in, osp)
}
