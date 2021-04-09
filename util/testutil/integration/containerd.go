package integration

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/pkg/errors"
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
	extraEnv    []string // e.g. "PATH=/opt/containerd-1.4/bin:/usr/bin:..."
}

func (c *containerd) Name() string {
	return c.name
}

func (c *containerd) New(cfg *BackendConfig) (b Backend, cl func() error, err error) {
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

	tmpdir, err := ioutil.TempDir("", "bktest_containerd")
	if err != nil {
		return nil, nil, err
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
	if err := ioutil.WriteFile(configFile, []byte(config), 0644); err != nil {
		return nil, nil, err
	}

	cmd := exec.Command(c.containerd, "--config", configFile)
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

	buildkitdSock, stop, err := runBuildkitd(cfg, buildkitdArgs, cfg.Logs, 0, 0, c.extraEnv)
	if err != nil {
		printLogs(cfg.Logs, log.Println)
		return nil, nil, err
	}
	deferF.append(stop)

	return backend{
		address:           buildkitdSock,
		containerdAddress: address,
		rootless:          false,
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
