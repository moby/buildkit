package compression

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseAttributes(t *testing.T) {
	for _, tc := range []struct {
		name     string
		attrs    map[string]string
		expected Config
		err      string
	}{
		{
			name:     "default",
			attrs:    map[string]string{},
			expected: New(Default),
		},
		{
			name: "all",
			attrs: map[string]string{
				"compression":         "zstd",
				"force-compression":   "true",
				"compression-level":   "3",
				"compression-threads": "4",
			},
			expected: New(Zstd).SetForce(true).SetLevel(3).SetThreads(4),
		},
		{
			name:     "force without value",
			attrs:    map[string]string{"force-compression": ""},
			expected: New(Default).SetForce(true),
		},
		{
			name:     "threads all cpus",
			attrs:    map[string]string{"compression": "zstd", "compression-threads": "0"},
			expected: New(Zstd).SetThreads(0),
		},
		{
			name:     "threads single",
			attrs:    map[string]string{"compression": "zstd", "compression-threads": "1"},
			expected: New(Zstd).SetThreads(1),
		},
		{
			name:  "unsupported type",
			attrs: map[string]string{"compression": "lz4"},
			err:   "unsupported compression type lz4",
		},
		{
			name:  "non-bool force",
			attrs: map[string]string{"force-compression": "yes"},
			err:   "non-bool value yes specified for force-compression",
		},
		{
			name:  "non-integer level",
			attrs: map[string]string{"compression-level": "high"},
			err:   "non-integer value high specified for compression-level",
		},
		{
			name:  "non-integer threads",
			attrs: map[string]string{"compression-threads": "many"},
			err:   "non-integer value many specified for compression-threads",
		},
		{
			name:  "negative threads",
			attrs: map[string]string{"compression-threads": "-1"},
			err:   "negative value -1 specified for compression-threads",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := ParseAttributes(tc.attrs)
			if tc.err != "" {
				require.ErrorContains(t, err, tc.err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.expected, cfg)
		})
	}
}
