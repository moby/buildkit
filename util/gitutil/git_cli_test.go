package gitutil

import (
	"context"
	"os"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetGitSSHCommandUsesConfigPath(t *testing.T) {
	cmd := getGitSSHCommand("")
	require.Equal(t, "ssh -F "+os.DevNull+" -o StrictHostKeyChecking=no", cmd)

	cmd = getGitSSHCommand("/known-hosts")
	require.Equal(t, "ssh -F "+os.DevNull+" -o UserKnownHostsFile=/known-hosts", cmd)
}

func TestGitCLIConfigEnv(t *testing.T) {
	t.Setenv("HOME", "/tmp/home")
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg")
	t.Setenv("USERPROFILE", `C:\Users\tester`)
	t.Setenv("HOMEDRIVE", "C:")
	t.Setenv("HOMEPATH", `\Users\tester`)
	t.Setenv("GIT_CONFIG_GLOBAL", "/tmp/global-gitconfig")
	t.Setenv("GIT_CONFIG_SYSTEM", "/tmp/system-gitconfig")
	t.Setenv("SUDO_UID", "1000")

	t.Run("isolated by default", func(t *testing.T) {
		var got []string
		cli := NewGitCLI(WithExec(func(ctx context.Context, cmd *exec.Cmd) error {
			got = append([]string(nil), cmd.Env...)
			return nil
		}))
		_, err := cli.Run(context.Background(), "status")
		require.NoError(t, err)
		require.Contains(t, got, "GIT_CONFIG_NOSYSTEM=1")
		require.Contains(t, got, "HOME="+os.DevNull)
		require.Contains(t, got, "GIT_CONFIG_GLOBAL="+os.DevNull)
		require.NotContains(t, got, "HOME=/tmp/home")
		require.NotContains(t, got, "XDG_CONFIG_HOME=/tmp/xdg")
		require.NotContains(t, got, "GIT_CONFIG_GLOBAL=/tmp/global-gitconfig")
		require.NotContains(t, got, "GIT_CONFIG_SYSTEM=/tmp/system-gitconfig")
		require.NotContains(t, got, "SUDO_UID=1000")
	})

	t.Run("host git config opt-in", func(t *testing.T) {
		var got []string
		cli := NewGitCLI(
			WithHostGitConfig(),
			WithExec(func(ctx context.Context, cmd *exec.Cmd) error {
				got = append([]string(nil), cmd.Env...)
				return nil
			}),
		)
		_, err := cli.Run(context.Background(), "status")
		require.NoError(t, err)
		require.NotContains(t, got, "GIT_CONFIG_NOSYSTEM=1")
		require.NotContains(t, got, "HOME="+os.DevNull)
		require.NotContains(t, got, "GIT_CONFIG_GLOBAL="+os.DevNull)
		require.Contains(t, got, "HOME=/tmp/home")
		require.Contains(t, got, "XDG_CONFIG_HOME=/tmp/xdg")
		require.Contains(t, got, `USERPROFILE=C:\Users\tester`)
		require.Contains(t, got, "HOMEDRIVE=C:")
		require.Contains(t, got, `HOMEPATH=\Users\tester`)
		require.Contains(t, got, "GIT_CONFIG_GLOBAL=/tmp/global-gitconfig")
		require.Contains(t, got, "GIT_CONFIG_SYSTEM=/tmp/system-gitconfig")
		require.Contains(t, got, "SUDO_UID=1000")
	})
}

func TestGitCLIAdvice(t *testing.T) {
	run := func(t *testing.T) (env, args []string) {
		cli := NewGitCLI(WithExec(func(ctx context.Context, cmd *exec.Cmd) error {
			env = append([]string(nil), cmd.Env...)
			args = append([]string(nil), cmd.Args...)
			return nil
		}))
		_, err := cli.Run(context.Background(), "status")
		require.NoError(t, err)
		return env, args
	}

	t.Run("silenced by default", func(t *testing.T) {
		env, args := run(t)
		// GIT_ADVICE=0 disables all advice hints on Git >= 2.45.
		require.Contains(t, env, "GIT_ADVICE=0")
		// advice.detachedHead=false keeps the detached-HEAD checkout quiet
		// on older Git releases that predate GIT_ADVICE.
		require.Contains(t, args, "advice.detachedHead=false")
	})

	t.Run("restored when GIT_ADVICE is set for debugging", func(t *testing.T) {
		t.Setenv("GIT_ADVICE", "1")
		env, args := run(t)
		require.Contains(t, env, "GIT_ADVICE=1")
		require.NotContains(t, env, "GIT_ADVICE=0")
		// The detached-HEAD override is dropped so the operator sees the
		// full advice output while debugging.
		require.NotContains(t, args, "advice.detachedHead=false")
	})
}
