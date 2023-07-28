package integration

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
<<<<<<< HEAD
	"path/filepath"
	"strings"
=======
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
>>>>>>> origin/v0.10
	"time"

	"github.com/docker/docker/client"
	"github.com/moby/buildkit/cmd/buildkitd/config"
<<<<<<< HEAD
	"github.com/moby/buildkit/util/testutil/dockerd"
	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
)

// InitDockerdWorker registers a dockerd worker with the global registry.
func InitDockerdWorker() {
	Register(&Moby{
		ID:         "dockerd",
		IsRootless: false,
		Unsupported: []string{
			FeatureCacheExport,
			FeatureCacheImport,
			FeatureCacheBackendAzblob,
			FeatureCacheBackendGha,
			FeatureCacheBackendLocal,
			FeatureCacheBackendRegistry,
			FeatureCacheBackendS3,
			FeatureDirectPush,
			FeatureImageExporter,
			FeatureMultiCacheExport,
			FeatureMultiPlatform,
			FeatureOCIExporter,
			FeatureOCILayout,
			FeatureProvenance,
			FeatureSBOM,
			FeatureSecurityMode,
			FeatureCNINetwork,
		},
	})
	Register(&Moby{
		ID:                    "dockerd-containerd",
		IsRootless:            false,
		ContainerdSnapshotter: true,
		Unsupported: []string{
			FeatureSecurityMode,
			FeatureCNINetwork,
		},
	})
}

type Moby struct {
	ID         string
	IsRootless bool

	ContainerdSnapshotter bool

	Unsupported []string
}

func (c Moby) Name() string {
	return c.ID
}

func (c Moby) Rootless() bool {
	return c.IsRootless
}

func (c Moby) New(ctx context.Context, cfg *BackendConfig) (b Backend, cl func() error, err error) {
=======
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/util/testutil/dockerd"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"golang.org/x/sync/errgroup"
)

const (
	shortLen      = 12
	dockerdBinary = "dockerd"
)

// InitDockerdWorker registers a dockerd worker with the global registry.
func InitDockerdWorker() {
	Register(&moby{})
}

type moby struct{}

func (c moby) Name() string {
	return dockerdBinary
}

func (c moby) New(ctx context.Context, cfg *BackendConfig) (b Backend, cl func() error, err error) {
>>>>>>> origin/v0.10
	if err := requireRoot(); err != nil {
		return nil, nil, err
	}

	bkcfg, err := config.LoadFile(cfg.ConfigFile)
	if err != nil {
		return nil, nil, errors.Wrapf(err, "failed to load buildkit config file %s", cfg.ConfigFile)
	}

<<<<<<< HEAD
	dcfg := dockerd.Config{
		Features: map[string]bool{
			"containerd-snapshotter": c.ContainerdSnapshotter,
		},
	}
=======
	dcfg := dockerd.Config{}
>>>>>>> origin/v0.10
	if reg, ok := bkcfg.Registries["docker.io"]; ok && len(reg.Mirrors) > 0 {
		for _, m := range reg.Mirrors {
			dcfg.Mirrors = append(dcfg.Mirrors, "http://"+m)
		}
	}
	if bkcfg.Entitlements != nil {
		for _, e := range bkcfg.Entitlements {
			switch e {
			case "network.host":
				dcfg.Builder.Entitlements.NetworkHost = true
			case "security.insecure":
				dcfg.Builder.Entitlements.SecurityInsecure = true
			}
		}
	}

	dcfgdt, err := json.Marshal(dcfg)
	if err != nil {
		return nil, nil, errors.Wrapf(err, "failed to marshal dockerd config")
	}

	deferF := &multiCloser{}
	cl = deferF.F()

	defer func() {
		if err != nil {
			deferF.F()()
			cl = nil
		}
	}()

	var proxyGroup errgroup.Group
	deferF.append(proxyGroup.Wait)

	workDir, err := os.MkdirTemp("", "integration")
	if err != nil {
		return nil, nil, err
	}

<<<<<<< HEAD
	d, err := dockerd.NewDaemon(workDir)
	if err != nil {
		return nil, nil, errors.Errorf("new daemon error: %q, %s", err, formatLogs(cfg.Logs))
	}

	dockerdConfigFile := filepath.Join(workDir, "daemon.json")
	if err := os.WriteFile(dockerdConfigFile, dcfgdt, 0644); err != nil {
		return nil, nil, err
	}

	dockerdFlags := []string{
		"--config-file", dockerdConfigFile,
		"--userland-proxy=false",
		"--tls=false",
		"--debug",
	}
	if s := os.Getenv("BUILDKIT_INTEGRATION_DOCKERD_FLAGS"); s != "" {
		dockerdFlags = append(dockerdFlags, strings.Split(strings.TrimSpace(s), "\n")...)
	}

	err = d.StartWithError(cfg.Logs, dockerdFlags...)
	if err != nil {
		return nil, nil, err
	}
	deferF.append(d.StopWithError)

	if err := waitUnix(d.Sock(), 5*time.Second, nil); err != nil {
		return nil, nil, errors.Errorf("dockerd did not start up: %q, %s", err, formatLogs(cfg.Logs))
=======
	dockerdBinaryPath, err := exec.LookPath(dockerdBinary)
	if err != nil {
		return nil, nil, errors.Wrapf(err, "could not find docker binary in $PATH")
	}

	id := "d" + identity.NewID()[:shortLen]
	dir := filepath.Join(workDir, id)
	daemonFolder, err := filepath.Abs(dir)
	if err != nil {
		return nil, nil, err
	}
	daemonRoot := filepath.Join(daemonFolder, "root")
	if err := os.MkdirAll(daemonRoot, 0755); err != nil {
		return nil, nil, errors.Wrapf(err, "failed to create daemon root %q", daemonRoot)
>>>>>>> origin/v0.10
	}
	execRoot := filepath.Join(os.TempDir(), "dxr", id)
	daemonSocket := "unix://" + filepath.Join(daemonFolder, "docker.sock")

	dockerdConfigFile := filepath.Join(workDir, "daemon.json")
	if err := os.WriteFile(dockerdConfigFile, dcfgdt, 0644); err != nil {
		return nil, nil, err
	}

	cmd := exec.Command(dockerdBinaryPath, []string{
		"--config-file", dockerdConfigFile,
		"--data-root", daemonRoot,
		"--exec-root", execRoot,
		"--pidfile", filepath.Join(daemonFolder, "docker.pid"),
		"--host", daemonSocket,
		"--userland-proxy=false",
		"--containerd-namespace", id,
		"--containerd-plugins-namespace", id + "p",
		"--bip", "10.66.66.1/24",
		"--default-address-pool", "base=10.66.66.0/16,size=24",
		"--debug",
	}...)
	cmd.Env = append(os.Environ(), "DOCKER_SERVICE_PREFER_OFFLINE_IMAGE=1", "BUILDKIT_DEBUG_EXEC_OUTPUT=1", "BUILDKIT_DEBUG_PANIC_ON_ERROR=1")
	cmd.SysProcAttr = getSysProcAttr()

	dockerdStop, err := startCmd(cmd, cfg.Logs)
	if err != nil {
		return nil, nil, errors.Wrapf(err, "dockerd startcmd error: %s", formatLogs(cfg.Logs))
	}
	if err := waitUnix(daemonSocket, 15*time.Second); err != nil {
		dockerdStop()
		return nil, nil, errors.Wrapf(err, "dockerd did not start up: %s", formatLogs(cfg.Logs))
	}
	deferF.append(dockerdStop)

<<<<<<< HEAD
	dockerAPI, err := client.NewClientWithOpts(client.WithHost(d.Sock()))
=======
	ctx, cancel := context.WithCancel(context.Background())
	deferF.append(func() error { cancel(); return nil })

	dockerAPI, err := client.NewClientWithOpts(
		client.FromEnv,
		client.WithHost(daemonSocket),
	)
>>>>>>> origin/v0.10
	if err != nil {
		return nil, nil, errors.Wrapf(err, "dockerd client api error: %s", formatLogs(cfg.Logs))
	}
	deferF.append(dockerAPI.Close)

	err = waitForAPI(ctx, dockerAPI, 5*time.Second)
	if err != nil {
		return nil, nil, errors.Wrapf(err, "dockerd client api timed out: %s", formatLogs(cfg.Logs))
	}

	// Create a file descriptor to be used as a Unix domain socket.
	// Remove it immediately (the name will still be valid for the socket) so that
	// we don't leave files all over the users tmp tree.
	f, err := os.CreateTemp("", "buildkit-integration")
	if err != nil {
		return
	}
	localPath := f.Name()
	f.Close()
	os.Remove(localPath)

	listener, err := net.Listen("unix", localPath)
	if err != nil {
		return nil, nil, errors.Wrapf(err, "dockerd listener error: %s", formatLogs(cfg.Logs))
	}
	deferF.append(listener.Close)

	proxyGroup.Go(func() error {
		for {
			tmpConn, err := listener.Accept()
			if err != nil {
				// Ignore the error from accept which is always a system error.
				return nil
			}
			conn, err := dockerAPI.DialHijack(ctx, "/grpc", "h2c", nil)
			if err != nil {
				if errors.Is(err, syscall.ECONNRESET) || errors.Is(err, net.ErrClosed) {
					logrus.Warn("dockerd conn already closed: ", err)
					return nil
				}
				return errors.Wrap(err, "dockerd grpc conn error")
			}

			proxyGroup.Go(func() error {
				_, err := io.Copy(conn, tmpConn)
				if err != nil {
					logrus.Warn("dockerd proxy error: ", err)
					return nil
				}
				return tmpConn.Close()
			})
			proxyGroup.Go(func() error {
				_, err := io.Copy(tmpConn, conn)
				if err != nil {
					logrus.Warn("dockerd proxy error: ", err)
					return nil
				}
				return conn.Close()
			})
		}
	})

	return backend{
<<<<<<< HEAD
		address:             "unix://" + listener.Addr().String(),
		dockerAddress:       d.Sock(),
		rootless:            c.IsRootless,
		isDockerd:           true,
		unsupportedFeatures: c.Unsupported,
	}, cl, nil
}

func (c Moby) Close() error {
	return nil
}

=======
		address:   "unix://" + listener.Addr().String(),
		rootless:  false,
		isDockerd: true,
	}, cl, nil
}

>>>>>>> origin/v0.10
func waitForAPI(ctx context.Context, apiClient *client.Client, d time.Duration) error {
	step := 50 * time.Millisecond
	i := 0
	for {
		if _, err := apiClient.Ping(ctx); err == nil {
			break
		}
		i++
		if time.Duration(i)*step > d {
			return errors.New("failed to connect to /_ping endpoint")
		}
		time.Sleep(step)
	}
	return nil
}

<<<<<<< HEAD
func IsTestDockerd() bool {
	return os.Getenv("TEST_DOCKERD") == "1"
}

func IsTestDockerdMoby(sb Sandbox) bool {
	b, err := getBackend(sb)
	if err != nil {
		return false
	}
	return b.isDockerd && sb.Name() == "dockerd"
}
=======
func SkipIfDockerd(t *testing.T, sb Sandbox, reason ...string) {
	t.Helper()
	sbx, ok := sb.(*sandbox)
	if !ok {
		t.Fatalf("invalid sandbox type %T", sb)
	}
	b, ok := sbx.Backend.(backend)
	if !ok {
		t.Fatalf("invalid backend type %T", b)
	}
	if b.isDockerd {
		t.Skipf("dockerd worker can not currently run this test due to missing features (%s)", strings.Join(reason, ", "))
	}
}

func IsTestDockerd() bool {
	return os.Getenv("TEST_DOCKERD") == "1"
}
>>>>>>> origin/v0.10
