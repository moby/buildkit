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
