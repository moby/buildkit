package main

import (
	"context"
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
	return cmd.Run(context.Background(), append([]string{"buildkitd"}, args...))
}
