package main

import (
	"context"
	"os"
	"testing"

	"github.com/moby/buildkit/cmd/buildkitd/config"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v3"
)

func TestApplyMainFlagsProxyNetwork(t *testing.T) {
	cfg := config.Config{}
	err := runApplyMainFlags(t, []string{"--proxy-network"}, &cfg)
	require.NoError(t, err)
	require.True(t, cfg.ProxyNetwork)
}

func TestApplyMainFlagsProxyNetworkOverridesConfig(t *testing.T) {
	cfg := config.Config{ProxyNetwork: true}
	err := runApplyMainFlags(t, []string{"--proxy-network=false"}, &cfg)
	require.NoError(t, err)
	require.False(t, cfg.ProxyNetwork)
}

func runApplyMainFlags(t *testing.T, args []string, cfg *config.Config) error {
	t.Helper()

	cmd := &cli.Command{
		Name: "buildkitd",
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name: "proxy-network",
			},
		},
		Action: func(_ context.Context, cmd *cli.Command) error {
			return applyMainFlags(cmd, cfg, nil)
		},
	}
	return cmd.Run(t.Context(), append([]string{"buildkitd"}, args...))
}

func TestConfigFlag(t *testing.T) {
	// Clear any ambient value so the default case is deterministic.
	if v, ok := os.LookupEnv("BUILDKITD_CONFIG"); ok {
		require.NoError(t, os.Unsetenv("BUILDKITD_CONFIG"))
		t.Cleanup(func() { os.Setenv("BUILDKITD_CONFIG", v) }) //nolint:usetesting // cannot use t.Setenv for unsetting env-vars.
	}

	testCases := []struct {
		name     string
		env      string
		args     []string
		expected string
	}{
		{
			name:     "default",
			expected: defaultConfigPath(),
		},
		{
			name:     "flag",
			args:     []string{"--config", "/tmp/flag.toml"},
			expected: "/tmp/flag.toml",
		},
		{
			name:     "env",
			env:      "/tmp/env.toml",
			expected: "/tmp/env.toml",
		},
		{
			name:     "flag overrides env",
			env:      "/tmp/env.toml",
			args:     []string{"--config", "/tmp/flag.toml"},
			expected: "/tmp/flag.toml",
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.env != "" {
				t.Setenv("BUILDKITD_CONFIG", tc.env)
			}
			require.Equal(t, tc.expected, runConfigFlag(t, tc.args))
		})
	}
}

func runConfigFlag(t *testing.T, args []string) string {
	t.Helper()

	var configPath string
	cmd := &cli.Command{
		Name: "buildkitd",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "config",
				Value:   defaultConfigPath(),
				Sources: cli.EnvVars("BUILDKITD_CONFIG"),
			},
		},
		Action: func(_ context.Context, cmd *cli.Command) error {
			configPath = cmd.String("config")
			return nil
		},
	}
	require.NoError(t, cmd.Run(t.Context(), append([]string{"buildkitd"}, args...)))
	return configPath
}
