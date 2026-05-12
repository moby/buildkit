package main

import (
	"flag"
	"testing"

	"github.com/moby/buildkit/cmd/buildkitd/config"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli"
)

func TestApplyMainFlagsProxyNetwork(t *testing.T) {
	fs := flag.NewFlagSet("buildkitd", flag.ContinueOnError)
	fs.Bool("proxy-network", false, "")
	require.NoError(t, fs.Set("proxy-network", "true"))

	cfg := config.Config{}
	err := applyMainFlags(cli.NewContext(cli.NewApp(), fs, nil), &cfg, nil)
	require.NoError(t, err)
	require.True(t, cfg.ProxyNetwork)
}

func TestApplyMainFlagsProxyNetworkOverridesConfig(t *testing.T) {
	fs := flag.NewFlagSet("buildkitd", flag.ContinueOnError)
	fs.Bool("proxy-network", false, "")
	require.NoError(t, fs.Set("proxy-network", "false"))

	cfg := config.Config{ProxyNetwork: true}
	err := applyMainFlags(cli.NewContext(cli.NewApp(), fs, nil), &cfg, nil)
	require.NoError(t, err)
	require.False(t, cfg.ProxyNetwork)
}
