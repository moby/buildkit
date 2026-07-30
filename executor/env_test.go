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
