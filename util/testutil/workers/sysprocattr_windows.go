//go:build windows
// +build windows

package workers

import (
	"path/filepath"
	"strings"
	"syscall"
)

func getSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{}
}

func getBuildkitdAddr(tmpdir string) string {
	return "npipe:////./pipe/buildkitd-" + filepath.Base(tmpdir)
}

func getTraceSocketPath(tmpdir string) string {
	return `\\.\pipe\buildkit-otel-grpc-` + filepath.Base(tmpdir)
}

func getContainerdSock(tmpdir string) string {
	return `\\.\pipe\containerd-` + filepath.Base(tmpdir)
}

func getContainerdDebugSock(tmpdir string) string {
	return `\\.\pipe\containerd-` + filepath.Base(tmpdir) + `debug`
}

func getBuildkitdArgs(address string) []string {
	address = filepath.ToSlash(address)
	if !strings.HasPrefix(address, "npipe://") {
		address = "npipe://" + address
	}
	return []string{"buildkitd",
		"--containerd-worker-gc=false",
		"--containerd-worker=true",
		"--containerd-worker-addr", address,
		"--containerd-worker-labels=org.mobyproject.buildkit.worker.sandbox=true", // Include use of --containerd-worker-labels to trigger https://github.com/moby/buildkit/pull/603
	}
}
