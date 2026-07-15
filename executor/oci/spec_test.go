package oci

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEnvName(t *testing.T) {
	require.Equal(t, "FOO", envName("FOO=bar"))
	require.Equal(t, "FOO", envName("FOO="))
	require.Equal(t, "FOO", envName("FOO"))
	require.Equal(t, "", envName("=bar"))
}

func TestAppendMissingTracingEnv(t *testing.T) {
	t.Run("appends all tracing vars when none are set", func(t *testing.T) {
		got := appendMissingTracingEnv([]string{"PATH=/usr/bin"})
		require.Equal(t, append([]string{"PATH=/usr/bin"}, tracingEnvVars...), got)
	})

	t.Run("keeps a user-provided OTEL_TRACES_EXPORTER (e.g. none)", func(t *testing.T) {
		got := appendMissingTracingEnv([]string{"OTEL_TRACES_EXPORTER=none"})
		require.Contains(t, got, "OTEL_TRACES_EXPORTER=none")
		require.NotContains(t, got, "OTEL_TRACES_EXPORTER=otlp")
		// the remaining tracing vars are still injected
		for _, e := range tracingEnvVars {
			if envName(e) != "OTEL_TRACES_EXPORTER" {
				require.Contains(t, got, e)
			}
		}
	})

	t.Run("does not override any user-provided tracing var", func(t *testing.T) {
		var env []string
		for _, e := range tracingEnvVars {
			env = append(env, envName(e)+"=custom")
		}
		require.Equal(t, env, appendMissingTracingEnv(env))
	})
}
