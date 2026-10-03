package sshutil

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
)

// BuildProbe compiles once per test for Windows and the worker architecture.
func BuildProbe(t *testing.T, arch string) []byte {
	t.Helper()
	require.Equal(t, "windows", runtime.GOOS, "SSH probe requires a Windows test host")
	require.NotEmpty(t, arch, "worker architecture")
	_, source, _, ok := runtime.Caller(0)
	require.True(t, ok)
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", ".."))
	// -trimpath builds omit the original source directory. The integration
	// harness still needs the checkout and Go toolchain to build the probe.
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		root, err = os.Getwd()
		require.NoError(t, err)
		for {
			if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
				break
			}
			parent := filepath.Dir(root)
			require.NotEqual(t, root, parent, "cannot find BuildKit checkout to compile SSH probe")
			root = parent
		}
	}
	path := filepath.Join(WorkDir(t), "sshprobe")
	ctx, cancel := context.WithTimeoutCause(t.Context(), 3*time.Minute, errors.New("SSH probe compilation timed out"))
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-mod=vendor", "-o", path, "./util/testutil/sshutil/probe/cmd")
	cmd.Dir = root
	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, "GOOS=") && !strings.HasPrefix(v, "GOARCH=") && !strings.HasPrefix(v, "CGO_ENABLED=") {
			cmd.Env = append(cmd.Env, v)
		}
	}
	cmd.Env = append(cmd.Env, "GOOS=windows", "GOARCH="+arch, "CGO_ENABLED=0")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "building SSH probe: %s", out)
	dt, err := os.ReadFile(path)
	require.NoError(t, err)
	return dt
}
