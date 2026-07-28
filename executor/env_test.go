package executor

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReplaceEnv(t *testing.T) {
	env := []string{
		"FOO=one",
		"HTTP_PROXY=http://upstream.example",
		"http_proxy=http://upstream.example",
		"NO_PROXY=example.com",
		"BAR=two",
	}
	replacement := []string{
		"HTTP_PROXY=http://buildkit-proxy",
		"http_proxy=http://buildkit-proxy",
		"NO_PROXY=localhost",
	}

	require.Equal(t, []string{
		"FOO=one",
		"BAR=two",
		"HTTP_PROXY=http://buildkit-proxy",
		"http_proxy=http://buildkit-proxy",
		"NO_PROXY=localhost",
	}, ReplaceEnv(env, replacement))
}

func TestFilterProxyEnv(t *testing.T) {
	require.Equal(t, []string{
		"HTTP_PROXY=http://buildkit-proxy",
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
