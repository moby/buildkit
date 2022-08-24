package integration

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

func InitContainerdWorker() {
	Register(&containerd{
		name:       "containerd",
		containerd: "containerd",
	})
	// defined in Dockerfile
	// e.g. `containerd-1.1=/opt/containerd-1.1/bin,containerd-42.0=/opt/containerd-42.0/bin`
	if s := os.Getenv("BUILDKIT_INTEGRATION_CONTAINERD_EXTRA"); s != "" {
		entries := strings.Split(s, ",")
		for _, entry := range entries {
			pair := strings.Split(strings.TrimSpace(entry), "=")
			if len(pair) != 2 {
				panic(errors.Errorf("unexpected BUILDKIT_INTEGRATION_CONTAINERD_EXTRA: %q", s))
			}
			name, bin := pair[0], pair[1]
			Register(&containerd{
				name:       name,
				containerd: filepath.Join(bin, "containerd"),
				// override PATH to make sure that the expected version of the shim binary is used
				extraEnv: []string{fmt.Sprintf("PATH=%s:%s", bin, os.Getenv("PATH"))},
			})
		}
	}

	// the rootless uid is defined in Dockerfile
	if s := os.Getenv("BUILDKIT_INTEGRATION_ROOTLESS_IDPAIR"); s != "" {
		var uid, gid int
		if _, err := fmt.Sscanf(s, "%d:%d", &uid, &gid); err != nil {
			logrus.Fatalf("unexpected BUILDKIT_INTEGRATION_ROOTLESS_IDPAIR: %q", s)
		}
		if rootlessSupported(uid) {
			Register(&containerd{
				name:        "containerd-rootless",
				containerd:  "containerd",
				uid:         uid,
				gid:         gid,
				snapshotter: "native", // TODO: test with fuse-overlayfs as well, or automatically determine snapshotter
			})
		}
	}

	if s := os.Getenv("BUILDKIT_INTEGRATION_SNAPSHOTTER"); s != "" {
		Register(&containerd{
			name:        fmt.Sprintf("containerd-snapshotter-%s", s),
			containerd:  "containerd",
			snapshotter: s,
		})
	}
}

type containerd struct {
	name        string
	containerd  string
	snapshotter string
	uid         int
	gid         int
	extraEnv    []string // e.g. "PATH=/opt/containerd-1.4/bin:/usr/bin:..."
}

func (c *containerd) Name() string {
	return c.name
}

func (c *containerd) Rootless() bool {
	return c.uid != 0
}

func (c *containerd) New(ctx context.Context, cfg *BackendConfig) (b Backend, cl func() error, err error) {
	if err := lookupBinary(c.containerd); err != nil {
		return nil, nil, err
	}
	if err := lookupBinary("buildkitd"); err != nil {
		return nil, nil, err
	}
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

	rootless := false
	if c.uid != 0 {
		if c.gid == 0 {
			return nil, nil, errors.Errorf("unsupported id pair: uid=%d, gid=%d", c.uid, c.gid)
		}
		rootless = true
	}

	tmpdir, err := os.MkdirTemp("", "bktest_containerd")
	if err != nil {
		return nil, nil, err
	}
	if rootless {
		if err := os.Chown(tmpdir, c.uid, c.gid); err != nil {
			return nil, nil, err
		}
	}

	deferF.append(func() error { return os.RemoveAll(tmpdir) })

	address := filepath.Join(tmpdir, "containerd.sock")
	config := fmt.Sprintf(`root = %q
state = %q
# CRI plugins listens on 10010/tcp for stream server.
# We disable CRI plugin so that multiple instance can run simultaneously.
disabled_plugins = ["cri"]

[grpc]
  address = %q

[debug]
  level = "debug"
  address = %q
`, filepath.Join(tmpdir, "root"), filepath.Join(tmpdir, "state"), address, filepath.Join(tmpdir, "debug.sock"))

	var snBuildkitdArgs []string
	if c.snapshotter != "" {
		snBuildkitdArgs = append(snBuildkitdArgs,
			fmt.Sprintf("--containerd-worker-snapshotter=%s", c.snapshotter))
		if c.snapshotter == "stargz" {
			snPath, snCl, err := runStargzSnapshotter(cfg)
			if err != nil {
				return nil, nil, err
			}
			deferF.append(snCl)
			config = fmt.Sprintf(`%s

[proxy_plugins]
  [proxy_plugins.stargz]
    type = "snapshot"
    address = %q
`, config, snPath)
		}
	}

	configFile := filepath.Join(tmpdir, "config.toml")
	if err := os.WriteFile(configFile, []byte(config), 0644); err != nil {
		return nil, nil, err
	}

	containerdArgs := []string{c.containerd, "--config", configFile}
	rootlessKitState := filepath.Join(tmpdir, "rootlesskit-containerd")
	if rootless {
		containerdArgs = append(append([]string{"sudo", "-u", fmt.Sprintf("#%d", c.uid), "-i",
			fmt.Sprintf("CONTAINERD_ROOTLESS_ROOTLESSKIT_STATE_DIR=%s", rootlessKitState),
			// Integration test requires the access to localhost of the host network namespace.
			// TODO: remove these configurations
			"CONTAINERD_ROOTLESS_ROOTLESSKIT_NET=host",
			"CONTAINERD_ROOTLESS_ROOTLESSKIT_PORT_DRIVER=none",
			"CONTAINERD_ROOTLESS_ROOTLESSKIT_FLAGS=--mtu=0",
		}, c.extraEnv...), "containerd-rootless.sh", "-c", configFile)
	}

	cmd := exec.Command(containerdArgs[0], containerdArgs[1:]...)
	cmd.Env = append(os.Environ(), c.extraEnv...)

	ctdStop, err := startCmd(cmd, cfg.Logs)
	if err != nil {
		return nil, nil, err
	}
	if err := waitUnix(address, 10*time.Second); err != nil {
		ctdStop()
		return nil, nil, errors.Wrapf(err, "containerd did not start up: %s", formatLogs(cfg.Logs))
	}
	deferF.append(ctdStop)

	buildkitdArgs := append([]string{"buildkitd",
		"--oci-worker=false",
		"--containerd-worker-gc=false",
		"--containerd-worker=true",
		"--containerd-worker-addr", address,
		"--containerd-worker-labels=org.mobyproject.buildkit.worker.sandbox=true", // Include use of --containerd-worker-labels to trigger https://github.com/moby/buildkit/pull/603
	}, snBuildkitdArgs...)

	if runtime.GOOS != "windows" && c.snapshotter != "native" {
		c.extraEnv = append(c.extraEnv, "BUILDKIT_DEBUG_FORCE_OVERLAY_DIFF=true")
	}
	if rootless {
		pidStr, err := os.ReadFile(filepath.Join(rootlessKitState, "child_pid"))
		if err != nil {
			return nil, nil, err
		}
		pid, err := strconv.ParseInt(string(pidStr), 10, 64)
		if err != nil {
			return nil, nil, err
		}
		buildkitdArgs = append([]string{"sudo", "-u", fmt.Sprintf("#%d", c.uid), "-i", "--", "exec",
			"nsenter", "-U", "--preserve-credentials", "-m", "-t", fmt.Sprintf("%d", pid)},
			append(buildkitdArgs, "--containerd-worker-snapshotter=native")...)
	}
	buildkitdSock, stop, err := runBuildkitd(ctx, cfg, buildkitdArgs, cfg.Logs, c.uid, c.gid, c.extraEnv)
	if err != nil {
		printLogs(cfg.Logs, log.Println)
		return nil, nil, err
	}
	deferF.append(stop)

	return backend{
		address:           buildkitdSock,
		containerdAddress: address,
		rootless:          rootless,
		snapshotter:       c.snapshotter,
	}, cl, nil
}

func formatLogs(m map[string]*bytes.Buffer) string {
	var ss []string
	for k, b := range m {
		if b != nil {
			ss = append(ss, fmt.Sprintf("%q:%q", k, b.String()))
		}
	}
	return strings.Join(ss, ",")
}
