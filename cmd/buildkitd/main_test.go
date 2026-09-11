package main

import (
	"context"
	"path/filepath"
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

func TestLoadConfigFile(t *testing.T) {
	fp := filepath.Join(t.TempDir(), "buildkitd.toml")

	_, err := runLoadConfigFile(t, fp, []string{"--config", fp})
	require.ErrorContains(t, err, fp)

	_, err = runLoadConfigFile(t, fp, nil)
	require.NoError(t, err)
}

func runLoadConfigFile(t *testing.T, defaultPath string, args []string) (config.Config, error) {
	t.Helper()

	var cfg config.Config
	cmd := &cli.Command{
		Name: "buildkitd",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "config",
				Value: defaultPath,
			},
		},
		Action: func(_ context.Context, cmd *cli.Command) error {
			var err error
			cfg, err = loadConfigFile(cmd)
			return err
		},
	}
	return cfg, cmd.Run(t.Context(), append([]string{"buildkitd"}, args...))
}
