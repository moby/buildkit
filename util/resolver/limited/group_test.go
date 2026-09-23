package limited

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetMaxConcurrency(t *testing.T) {
	tests := []struct {
		name     string
		envValue string
		expected int64
	}{
		{
			name:     "default when unset",
			envValue: "",
			expected: 4,
		},
		{
			name:     "custom valid value",
			envValue: "20",
			expected: 20,
		},
		{
			name:     "minimum valid value",
			envValue: "1",
			expected: 1,
		},
		{
			name:     "zero falls back to default",
			envValue: "0",
			expected: 4,
		},
		{
			name:     "negative falls back to default",
			envValue: "-1",
			expected: 4,
		},
		{
			name:     "non-numeric falls back to default",
			envValue: "abc",
			expected: 4,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envValue == "" {
				os.Unsetenv("BUILDKIT_MAX_REGISTRY_CONCURRENCY")
			} else {
				t.Setenv("BUILDKIT_MAX_REGISTRY_CONCURRENCY", tt.envValue)
			}
			result := getMaxConcurrency()
			require.Equal(t, tt.expected, result)
		})
	}
}
