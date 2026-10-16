package gitutil

import (
	"context"
	"io"
	"os"
	"os/exec"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetGitSSHCommandUsesConfigPath(t *testing.T) {
	cmd := getGitSSHCommand("")
	require.Equal(t, "ssh -F "+os.DevNull+" -o StrictHostKeyChecking=no", cmd)

	cmd = getGitSSHCommand("/known-hosts")
	require.Equal(t, "ssh -F "+os.DevNull+" -o UserKnownHostsFile=/known-hosts", cmd)
}

func TestArgsNoDepth(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "default",
			args: []string{"fetch", "--depth=1", "origin", "main"},
			want: []string{"fetch", "origin", "main"},
		},
		{
			name: "positive",
			args: []string{"fetch", "--depth=3", "--tags", "origin", "main"},
			want: []string{"fetch", "--tags", "origin", "main"},
		},
		{
			name: "separate argument",
			args: []string{"fetch", "--depth", "2", "origin", "main"},
			want: []string{"fetch", "origin", "main"},
		},
		{
			name: "ref after separator",
			args: []string{"fetch", "--depth=2", "origin", "--", "--depth=1"},
			want: []string{"fetch", "origin", "--", "--depth=1"},
		},
		{
			name: "invalid depth",
			args: []string{"fetch", "--depth=invalid", "origin"},
			want: []string{"fetch", "--depth=invalid", "origin"},
		},
		{
			name: "zero depth",
			args: []string{"fetch", "--depth=0", "origin"},
			want: []string{"fetch", "--depth=0", "origin"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := append([]string(nil), tc.args...)
			require.Equal(t, tc.want, argsNoDepth(tc.args))
			require.Equal(t, original, tc.args)
		})
	}
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
			WithExec(func(ctx context.Context, cmd *exec.Cmd) error {
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
		opts = append(opts, WithExec(func(ctx context.Context, cmd *exec.Cmd) error {
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

func TestGitCLIDebugCommands(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts []Option
		want string
	}{
		{
			name: "default",
		},
		{
			name: "disabled",
			opts: []Option{WithDebugCommands(false)},
		},
		{
			name: "enabled",
			opts: []Option{WithDebugCommands(true)},
			want: "+ git -c protocol.file.allow=user -c http.extraheader=xxxxx ls-remote https://xxxxx:xxxxx@example.com/repo.git\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stderr, err := os.CreateTemp(t.TempDir(), "stderr")
			require.NoError(t, err)
			t.Cleanup(func() { stderr.Close() })
			const remote = "https://user:pass@example.com/repo.git"
			const header = "http.extraheader=Authorization: Bearer secret"
			var gotArgs []string
			opts := append(tc.opts,
				WithArgs("-c", header),
				WithStreams(func(context.Context) (io.WriteCloser, io.WriteCloser, func()) {
					return nil, stderr, nil
				}),
				WithExec(func(_ context.Context, cmd *exec.Cmd) error {
					gotArgs = cmd.Args
					_, err := io.WriteString(cmd.Stdout, "result")
					return err
				}),
			)
			out, err := NewGitCLI(opts...).Run(t.Context(), "ls-remote", remote)
			require.NoError(t, err)
			require.Equal(t, "result", string(out))
			require.Equal(t, []string{"git", "-c", "protocol.file.allow=user", "-c", header, "ls-remote", remote}, gotArgs)
			trace, err := os.ReadFile(stderr.Name())
			require.NoError(t, err)
			require.Equal(t, tc.want, string(trace))
		})
	}
}

func TestGitCLIStreamsAllowNil(t *testing.T) {
	cli := NewGitCLI(
		WithStreams(func(context.Context) (io.WriteCloser, io.WriteCloser, func()) {
			return nil, nil, nil
		}),
		WithExec(func(context.Context, *exec.Cmd) error {
			return nil
		}),
	)
	_, err := cli.Run(t.Context(), "status")
	require.NoError(t, err)
}

func TestGitCLIQuiet(t *testing.T) {
	for _, debug := range []bool{false, true} {
		for _, command := range []string{"init", "fetch", "ls-remote", "for-each-ref"} {
			t.Run(command+"/debug="+strconv.FormatBool(debug), func(t *testing.T) {
				stdout, err := os.CreateTemp(t.TempDir(), "stdout")
				require.NoError(t, err)
				t.Cleanup(func() { stdout.Close() })
				stderr, err := os.CreateTemp(t.TempDir(), "stderr")
				require.NoError(t, err)
				t.Cleanup(func() { stderr.Close() })
				cli := NewGitCLI(
					WithQuiet(),
					WithDebugCommands(debug),
					WithStreams(func(context.Context) (io.WriteCloser, io.WriteCloser, func()) {
						return stdout, stderr, nil
					}),
					WithExec(func(_ context.Context, cmd *exec.Cmd) error {
						require.Equal(t, []string{"git", "-c", "protocol.file.allow=user", command}, cmd.Args)
						_, err := io.WriteString(cmd.Stdout, "query result\n")
						require.NoError(t, err)
						_, err = io.WriteString(cmd.Stderr, "warning\n")
						return err
					}),
				)
				out, err := cli.Run(t.Context(), command)
				require.NoError(t, err)
				require.Equal(t, "query result\n", string(out))
				logged, err := os.ReadFile(stdout.Name())
				require.NoError(t, err)
				require.Empty(t, logged)
				logged, err = os.ReadFile(stderr.Name())
				require.NoError(t, err)
				require.Contains(t, string(logged), "warning\n")
				if debug {
					require.Contains(t, string(logged), "+ git ")
				} else {
					require.Equal(t, "warning\n", string(logged))
				}
			})
		}
	}
}

func TestFormatDebugCommand(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		expected string
	}{
		{
			name: "quotes and redacts urls and config",
			args: []string{
				"git",
				"-c", "protocol.file.allow=user",
				"--work-tree", "/tmp/work tree",
				"--git-dir", "/tmp/work tree/.git",
				"-c", "http.https://github.com/.extraheader=Authorization: Basic c2VjcmV0",
				"-c", "core.abbrev=12",
				"fetch",
				"https://user:pass@example.com/repo.git",
				"refs/heads/main",
			},
			expected: `git -c protocol.file.allow=user --work-tree "/tmp/work tree" --git-dir "/tmp/work tree/.git" -c http.https://github.com/.extraheader=xxxxx -c core.abbrev=12 fetch https://xxxxx:xxxxx@example.com/repo.git refs/heads/main`,
		},
		{
			name: "redacts extraheader config only",
			args: []string{
				"git",
				"-c", "http.https://github.com/.extraheader=Authorization: Basic dXNlcjp0b2tlbg==",
				"-c", "core.abbrev=12",
				"fetch",
			},
			expected: `git -c http.https://github.com/.extraheader=xxxxx -c core.abbrev=12 fetch`,
		},
		{
			name: "redacts credentials in extraheader config key",
			args: []string{
				"git",
				"-c", "http.https://user:pass@example.com/.extraheader=Authorization: Basic dXNlcjp0b2tlbg==",
				"fetch",
			},
			expected: `git -c http.https://xxxxx:xxxxx@example.com/.extraheader=xxxxx fetch`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.expected, formatDebugCommand(tt.args))
		})
	}
}
