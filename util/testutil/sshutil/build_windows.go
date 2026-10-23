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
	"golang.org/x/sys/windows"
)

// BuildProbe compiles once per test for Windows and the worker architecture.
func BuildProbe(t *testing.T, arch string) []byte {
	return buildWindowsBinary(t, arch, "sshprobe", "./util/testutil/sshutil/probe/cmd")
}

// BuildOpenSSHCheck builds the real-client test helper for the worker architecture.
func BuildOpenSSHCheck(t *testing.T, arch string) []byte {
	return buildWindowsBinary(t, arch, "opensshcheck", "./util/testutil/sshutil/opensshcheck/cmd")
}

func openSSHPath(t *testing.T) string {
	t.Helper()
	systemDir, err := windows.GetSystemDirectory()
	require.NoError(t, err)
	path, err := findOpenSSHPath(systemDir)
	require.NoError(t, err, "Windows OpenSSH client is required")
	t.Logf("using Windows OpenSSH client %s", path)
	return path
}

func findOpenSSHPath(systemDir string) (string, error) {
	// Prefer the inbox client because CI's PATH may select Git's OpenSSH.
	path := filepath.Join(systemDir, "OpenSSH", "ssh.exe")
	_, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return exec.LookPath("ssh.exe")
	}
	if err != nil {
		return "", err
	}
	return path, nil
}

// OpenSSHRuntime returns the Windows OpenSSH client and its crypto runtime.
func OpenSSHRuntime(t *testing.T, arch string) ([]byte, []byte) {
	t.Helper()
	require.Equal(t, "windows", runtime.GOOS, "OpenSSH client requires a Windows test host")
	require.Equal(t, runtime.GOARCH, arch, "host OpenSSH client architecture must match the worker")
	path := openSSHPath(t)
	ctx, cancel := context.WithTimeoutCause(t.Context(), 30*time.Second, errors.New("checking Windows OpenSSH client timed out"))
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "-V").CombinedOutput()
	require.NoError(t, err, "checking Windows OpenSSH client: %s", out)
	require.Contains(t, string(out), "OpenSSH_for_Windows")
	client, err := os.ReadFile(path)
	require.NoError(t, err)
	sshDir := filepath.Dir(path)
	candidates := []string{filepath.Join(sshDir, "libcrypto.dll")}
	systemDir, err := windows.GetSystemDirectory()
	require.NoError(t, err)
	systemOpenSSHDir := filepath.Join(systemDir, "OpenSSH")
	if strings.EqualFold(sshDir, systemOpenSSHDir) {
		candidates = append(candidates, filepath.Join(filepath.Dir(sshDir), "libcrypto.dll"))
	}
	var crypto []byte
	for _, candidate := range candidates {
		crypto, err = os.ReadFile(candidate)
		if err == nil {
			return client, crypto
		}
		if !errors.Is(err, os.ErrNotExist) {
			require.NoError(t, err)
		}
	}
	require.FailNow(t, "Windows OpenSSH libcrypto runtime is required", "no libcrypto.dll found for %s", path)
	return nil, nil
}

func buildWindowsBinary(t *testing.T, arch, name, pkg string) []byte {
	t.Helper()
	require.Equal(t, "windows", runtime.GOOS, "Windows test binary requires a Windows test host")
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
	path := filepath.Join(WorkDir(t), name)
	ctx, cancel := context.WithTimeoutCause(t.Context(), 3*time.Minute, errors.Errorf("%s compilation timed out", name))
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-mod=vendor", "-o", path, pkg)
	cmd.Dir = root
	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, "GOOS=") && !strings.HasPrefix(v, "GOARCH=") && !strings.HasPrefix(v, "CGO_ENABLED=") {
			cmd.Env = append(cmd.Env, v)
		}
	}
	cmd.Env = append(cmd.Env, "GOOS=windows", "GOARCH="+arch, "CGO_ENABLED=0")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "building %s: %s", name, out)
	dt, err := os.ReadFile(path)
	require.NoError(t, err)
	return dt
}
