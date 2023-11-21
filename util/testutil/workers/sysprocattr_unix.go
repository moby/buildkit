//go:build !windows
// +build !windows

package workers

import (
	"path/filepath"
	"syscall"
)

func getSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		Setsid: true, // stretch sudo needs this for sigterm
	}
}

func getBuildkitdAddr(tmpdir string) string {
	return "unix://" + filepath.Join(tmpdir, "buildkitd.sock")
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

func getBuildkitdArgs(address string) []string {
	return []string{"buildkitd",
		"--oci-worker=false",
		"--containerd-worker-gc=false",
		"--containerd-worker=true",
		"--containerd-worker-addr", address,
		"--containerd-worker-labels=org.mobyproject.buildkit.worker.sandbox=true", // Include use of --containerd-worker-labels to trigger https://github.com/moby/buildkit/pull/603
	}
}
