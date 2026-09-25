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
		cli := NewGitCLI(WithExec(func(_ context.Context, cmd *exec.Cmd) error {
			got = append([]string(nil), cmd.Env...)
			return nil
		}))
		_, err := cli.Run(t.Context(), "status")
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
			WithExec(func(_ context.Context, cmd *exec.Cmd) error {
				got = append([]string(nil), cmd.Env...)
				return nil
			}),
		)
		_, err := cli.Run(t.Context(), "status")
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

func TestGitCLIAdviceOption(t *testing.T) {
	run := func(t *testing.T, opts ...Option) (env, args []string) {
		t.Helper()
		opts = append(opts, WithExec(func(_ context.Context, cmd *exec.Cmd) error {
			env = append([]string(nil), cmd.Env...)
			args = append([]string(nil), cmd.Args...)
			return nil
		}))
		cli := NewGitCLI(opts...)
		_, err := cli.Run(t.Context(), "status")
		require.NoError(t, err)
		return env, args
	}

	t.Run("unset by default", func(t *testing.T) {
		env, args := run(t)
		require.NotContains(t, env, "GIT_ADVICE=0")
		require.NotContains(t, env, "GIT_ADVICE=1")
		require.NotContains(t, args, "advice.detachedHead=false")
	})

	t.Run("disabled", func(t *testing.T) {
		env, args := run(t, WithGitAdvice(false))
		require.Contains(t, env, "GIT_ADVICE=0")
		require.Contains(t, args, "advice.detachedHead=false")
	})

	t.Run("enabled", func(t *testing.T) {
		env, args := run(t, WithGitAdvice(true))
		require.Contains(t, env, "GIT_ADVICE=1")
		require.NotContains(t, args, "advice.detachedHead=false")
	})
}
