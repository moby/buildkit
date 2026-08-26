package network

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFilterProxyEnv(t *testing.T) {
	require.Equal(t, []string{
		"HTTP_PROXY=http://buildkit-proxy",
		"ALL_PROXY=http://initial-process-proxy",
		"all_proxy=http://initial-process-proxy",
		"NO_PROXY=localhost",
	}, FilterProxyEnv([]string{
		"PATH=/usr/bin",
		"HTTP_PROXY=http://buildkit-proxy",
		"FTP_PROXY=http://ftp-proxy",
		"ALL_PROXY=http://initial-process-proxy",
		"all_proxy=http://initial-process-proxy",
		"NO_PROXY=localhost",
	}))
}
