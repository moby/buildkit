//go:build windows
// +build windows

package workers

import (
	"path/filepath"
	"syscall"
)

func getSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{}
}

func getBuildkitdAddr(tmpdir string) string {
	return "//./pipe/buildkitd-" + filepath.Base(tmpdir)
}

func getTraceSocketPath(tmpdir string) string {
	return `\\.\pipe\buildkit-otel-grpc-` + filepath.Base(tmpdir)
}
