package gitutil

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFormatDebugCommand(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		expected string
	}{
		{
			name: "quotes and redacts urls and config",
			args: []string{
				"git",
				"-c", "protocol.file.allow=user",
				"--work-tree", "/tmp/work tree",
				"--git-dir", "/tmp/work tree/.git",
				"-c", "http.https://github.com/.extraheader=Authorization: Basic c2VjcmV0",
				"-c", "core.abbrev=12",
				"fetch",
				"https://user:pass@example.com/repo.git",
				"refs/heads/main",
			},
			expected: `git -c protocol.file.allow=user --work-tree "/tmp/work tree" --git-dir "/tmp/work tree/.git" -c http.https://github.com/.extraheader=xxxxx -c core.abbrev=12 fetch https://xxxxx:xxxxx@example.com/repo.git refs/heads/main`,
		},
		{
			name: "redacts extraheader config only",
			args: []string{
				"git",
				"-c", "http.https://github.com/.extraheader=Authorization: Basic dXNlcjp0b2tlbg==",
				"-c", "core.abbrev=12",
				"fetch",
			},
			expected: `git -c http.https://github.com/.extraheader=xxxxx -c core.abbrev=12 fetch`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.expected, formatDebugCommand(tt.args))
		})
	}
}
