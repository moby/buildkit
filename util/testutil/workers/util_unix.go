//go:build !windows

package workers

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/moby/buildkit/util/testutil/integration"
)

const buildkitdNetworkProtocol = "unix"

func applyBuildkitdPlatformFlags(args []string) []string {
	return append(args, "--oci-worker=false")
}

func requireRoot() error {
	if os.Getuid() != 0 {
		return fmt.Errorf("requires root"+": %w", integration.ErrRequirements)
	}
	return nil
}

func getSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		Setsid: true, // stretch sudo needs this for sigterm
	}
}

func getBuildkitdAddr(tmpdir string) string {
	return "unix://" + filepath.Join(tmpdir, "buildkitd.sock")
}

func getBuildkitdDebugAddr(tmpdir string) string {
	return "unix://" + filepath.Join(tmpdir, "buildkitd-debug.sock")
}

func getTraceSocketPath(tmpdir string) string {
	return filepath.Join(tmpdir, "otel-grpc.sock")
}

func getContainerdSock(tmpdir string) string {
	return filepath.Join(tmpdir, "containerd.sock")
}

func getContainerdDebugSock(tmpdir string) string {
	return filepath.Join(tmpdir, "debug.sock")
}

func mountInfo(tmpdir string) error {
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return fmt.Errorf("failed to open mountinfo"+": %w", err)
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		if strings.Contains(s.Text(), tmpdir) {
			return fmt.Errorf("leaked mountpoint for %s", tmpdir)
		}
	}
	return s.Err()
}

// moved here since os.Chown is not supported on Windows.
// see no-op counterpart in util_windows.go
func chown(name string, uid, gid int) error {
	return os.Chown(name, uid, gid)
}

func normalizeAddress(address string) string {
	// for parity with windows, no effect for unix
	return address
}

func applyDockerdPlatformFlags(flags []string, _ string) []string {
	flags = append(flags, "--userland-proxy=false")
	return flags
}

func getBuildkitdNetworkAddr(tmpdir string) string {
	return tmpdir
}
