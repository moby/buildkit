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

	"github.com/moby/buildkit/util/bklog"
	"github.com/pkg/errors"
)

func InitContainerdWorker() {
	Register(&Containerd{
		ID:         "containerd",
		Containerd: "containerd",
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
			Register(&Containerd{
				ID:         name,
				Containerd: filepath.Join(bin, "containerd"),
				// override PATH to make sure that the expected version of the shim binary is used
				ExtraEnv: []string{fmt.Sprintf("PATH=%s:%s", bin, os.Getenv("PATH"))},
			})
		}
	}

	// the rootless uid is defined in Dockerfile
	if s := os.Getenv("BUILDKIT_INTEGRATION_ROOTLESS_IDPAIR"); s != "" {
		var uid, gid int
		if _, err := fmt.Sscanf(s, "%d:%d", &uid, &gid); err != nil {
			bklog.L.Fatalf("unexpected BUILDKIT_INTEGRATION_ROOTLESS_IDPAIR: %q", s)
		}
		if rootlessSupported(uid) {
			Register(&Containerd{
				ID:          "containerd-rootless",
				Containerd:  "containerd",
				UID:         uid,
				GID:         gid,
				Snapshotter: "native",
			})
			Register(&Containerd{
				ID:          "containerd-rootless-snapshotter-fuse-overlayfs",
				Containerd:  "containerd",
				UID:         uid,
				GID:         gid,
				Snapshotter: "fuse-overlayfs",
			})
			Register(&Containerd{
				ID:          "containerd-rootless-snapshotter-fuse-overlayfs-disable-ovl-whiteout",
				Containerd:  "containerd",
				UID:         uid,
				GID:         gid,
				Snapshotter: "fuse-overlayfs",
				ExtraEnv:    []string{"FUSE_OVERLAYFS_DISABLE_OVL_WHITEOUT=1"},
			})
		}
	}

	if s := os.Getenv("BUILDKIT_INTEGRATION_SNAPSHOTTER"); s != "" {
		Register(&Containerd{
			ID:          fmt.Sprintf("containerd-snapshotter-%s", s),
			Containerd:  "containerd",
			Snapshotter: s,
		})
	}
}

type Containerd struct {
	ID          string
	Containerd  string
	Snapshotter string
	UID         int
	GID         int
	ExtraEnv    []string // e.g. "PATH=/opt/containerd-1.4/bin:/usr/bin:..."
}

func (c *Containerd) Name() string {
	return c.ID
}

func (c *Containerd) Rootless() bool {
	return c.UID != 0
}

func (c *Containerd) New(ctx context.Context, cfg *BackendConfig) (b Backend, cl func() error, err error) {
	if err := lookupBinary(c.Containerd); err != nil {
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
	if c.UID != 0 {
		if c.GID == 0 {
			return nil, nil, errors.Errorf("unsupported id pair: uid=%d, gid=%d", c.UID, c.GID)
		}
		rootless = true
	}

	tmpdir, err := os.MkdirTemp("", "bktest_containerd")
	if err != nil {
		return nil, nil, err
	}
	if rootless {
		if err := os.Chown(tmpdir, c.UID, c.GID); err != nil {
			return nil, nil, err
		}
	}

	deferF.append(func() error { return os.RemoveAll(tmpdir) })

	// Run rootlesskit if rootless mode
	rootlessKitState := ""
	if rootless {
		rootlessKitState, err = c.runRootlesskit(tmpdir, cfg, deferF)
		if err != nil {
			return nil, nil, err
		}
	}

	// Generate containerd config file
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
	if c.Snapshotter != "" {
		snBuildkitdArgs = append(snBuildkitdArgs,
			fmt.Sprintf("--containerd-worker-snapshotter=%s", c.Snapshotter))

		// Start Snapshotter plugin
		if err := c.runSnapshotterPlugin(&config, cfg, rootlessKitState, deferF); err != nil {
			return nil, nil, err
		}
	} else if rootless {
		snBuildkitdArgs = append(snBuildkitdArgs, "--containerd-worker-snapshotter=native")
	}

	// Write containerd config file
	configFile := filepath.Join(tmpdir, "config.toml")
	if err := os.WriteFile(configFile, []byte(config), 0644); err != nil {
		return nil, nil, err
	}

	// Start containerd
	containerdArgs := []string{c.Containerd, "--config", configFile}
	err = c.runContainerdProcess(cfg, rootlessKitState, containerdArgs, address, c.ExtraEnv, deferF)
	if err != nil {
		return nil, nil, err
	}

	buildkitdArgs := append([]string{"buildkitd",
		"--oci-worker=false",
		"--containerd-worker-gc=false",
		"--containerd-worker=true",
		"--containerd-worker-addr", address,
		"--containerd-worker-labels=org.mobyproject.buildkit.worker.sandbox=true", // Include use of --containerd-worker-labels to trigger https://github.com/moby/buildkit/pull/603
	}, snBuildkitdArgs...)

	if runtime.GOOS != "windows" && c.Snapshotter != "native" {
		c.ExtraEnv = append(c.ExtraEnv, "BUILDKIT_DEBUG_FORCE_OVERLAY_DIFF=true")
	}
	if rootless {
		buildkitdArgs, err = addRootlessArgs(buildkitdArgs, c.UID, rootlessKitState)
		if err != nil {
			return nil, nil, err
		}
	}
	buildkitdSock, stop, err := runBuildkitd(ctx, cfg, buildkitdArgs, cfg.Logs, c.UID, c.GID, c.ExtraEnv)
	if err != nil {
		printLogs(cfg.Logs, log.Println)
		return nil, nil, err
	}
	deferF.append(stop)

	return backend{
		address:           buildkitdSock,
		containerdAddress: address,
		rootless:          rootless,
		snapshotter:       c.Snapshotter,
	}, cl, nil
}

func (c *Containerd) runRootlesskit(tmpdir string, cfg *BackendConfig, deferF *multiCloser) (string, error) {
	rootlessKitState := filepath.Join(tmpdir, "rootlesskit-containerd")
	args := append(append([]string{"sudo", "-u", fmt.Sprintf("#%d", c.UID), "-i"}, c.ExtraEnv...),
		"rootlesskit",
		fmt.Sprintf("--state-dir=%s", rootlessKitState),
		// Integration test requires the access to localhost of the host network namespace.
		// TODO: remove these configurations
		"--net=host",
		"--disable-host-loopback",
		"--port-driver=none",
		"--copy-up=/etc",
		"--copy-up=/run",
		"--copy-up=/var/lib",
		"--propagation=rslave",
		"--slirp4netns-sandbox=auto",
		"--slirp4netns-seccomp=auto",
		"--mtu=0",
		"sh",
		"-c",
		"rm -rf /run/containerd ; sleep infinity")

	// Start rootlesskit
	// Don't put rootlessKitState as we are just starting rootlesskit, rootlessKitState won't contain child_pid
	err := c.runContainerdProcess(cfg, "", args, filepath.Join(rootlessKitState, "api.sock"), nil, deferF)
	if err != nil {
		return "", err
	}

	return rootlessKitState, nil
}

func (c *Containerd) runSnapshotterPlugin(config *string, cfg *BackendConfig, rootlessKitState string, deferF *multiCloser) error {
	var argsGenerator func(string, string) []string
	switch c.Snapshotter {
	case "stargz":
		argsGenerator = func(snPath string, snRoot string) []string {
			return []string{"containerd-stargz-grpc",
				"--log-level", "debug",
				"--address", snPath,
				"--root", snRoot,
			}
		}
	case "fuse-overlayfs":
		argsGenerator = func(snPath string, snRoot string) []string {
			return []string{"containerd-fuse-overlayfs-grpc", snPath, snRoot}
		}
	default:
		// No plugin to run
		return nil
	}

	snapshotterTmpDir, err := os.MkdirTemp("", fmt.Sprintf("bktest_containerd-%s-grpc", c.Snapshotter))
	if err != nil {
		return err
	}
	deferF.append(func() error { return os.RemoveAll(snapshotterTmpDir) })

	if err := os.Chown(snapshotterTmpDir, c.UID, c.GID); err != nil {
		return err
	}

	snPath := filepath.Join(snapshotterTmpDir, "snapshotter.sock")
	snRoot := filepath.Join(snapshotterTmpDir, "root")
	*config = c.generateSnapshotterConfig(*config, snPath)

	args := argsGenerator(snPath, snRoot)
	if err := lookupBinary(args[0]); err != nil {
		return err
	}

	err = c.runContainerdProcess(cfg, rootlessKitState, args, snPath, nil, deferF)
	if err != nil {
		return err
	}

	return nil
}

func (c *Containerd) generateSnapshotterConfig(config string, snPath string) string {
	return fmt.Sprintf(`%s

[proxy_plugins]
	[proxy_plugins.%s]
	type = "snapshot"
	address = %q
`, config, c.Snapshotter, snPath)
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

func (c *Containerd) runContainerdProcess(cfg *BackendConfig, rootlessKitState string, args []string, unixSocketToWait string, extraEnv []string, deferF *multiCloser) error {
	// If we are using rootlesskit, add arguments to run the process in rootless namespace
	if rootlessKitState != "" {
		var err error
		args, err = addRootlessArgs(args, c.UID, rootlessKitState)
		if err != nil {
			return err
		}
	}

	cmd := exec.Command(args[0], args[1:]...) //nolint:gosec // test utility
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	snStop, err := startCmd(cmd, cfg.Logs)
	if err != nil {
		return err
	}
	if err := waitUnix(unixSocketToWait, 10*time.Second, cmd); err != nil {
		snStop()
		return errors.Wrapf(err, "%s did not start up: %s", cmd.Path, formatLogs(cfg.Logs))
	}
	deferF.append(snStop)

	return nil
}

func addRootlessArgs(args []string, uid int, rootlessKitState string) ([]string, error) {
	pidStr, err := os.ReadFile(filepath.Join(rootlessKitState, "child_pid"))
	if err != nil {
		return args, err
	}
	pid, err := strconv.ParseInt(string(pidStr), 10, 64)
	if err != nil {
		return args, err
	}
	args = append([]string{"sudo", "-u", fmt.Sprintf("#%d", uid), "-i", "--", "exec",
		"nsenter", "-U", "--preserve-credentials", "-m", "-t", fmt.Sprintf("%d", pid)},
		args...)

	return args, nil
}
