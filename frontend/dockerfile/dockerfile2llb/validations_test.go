package dockerfile2llb

import (
	"testing"

	"github.com/moby/buildkit/frontend/dockerfile/linter"
	"github.com/moby/buildkit/frontend/dockerfile/parser"
	"github.com/moby/patternmatcher"
	"github.com/stretchr/testify/require"
)

func copySourceLintWarnings(t *testing.T, patterns []string, src string, isAdd bool) []string {
	t.Helper()

	pm, err := patternmatcher.New(patterns)
	require.NoError(t, err)

	var warnings []string
	lint := linter.New(&linter.Config{
		Warn: func(rulename, description, url, fmtmsg string, location []parser.Range) {
			warnings = append(warnings, fmtmsg)
		},
	})

	cfg := &copyConfig{
		isAddCommand:  isAdd,
		ignoreMatcher: pm,
		opt:           dispatchOpt{lint: lint},
	}
	require.NoError(t, validateCopySourcePath(src, cfg))
	return warnings
}

func TestValidateCopySourcePath(t *testing.T) {
	tests := []struct {
		name     string
		patterns []string
		src      string
		isAdd    bool
		expected []string
	}{
		{
			name:     "no dockerignore patterns",
			patterns: []string{},
			src:      "sub/a.txt",
		},
		{
			name:     "excluded file",
			patterns: []string{"sub"},
			src:      "sub/a.txt",
			expected: []string{`Attempting to Copy file "sub/a.txt" that is excluded by .dockerignore`},
		},
		{
			name:     "excluded file with add",
			patterns: []string{"sub"},
			src:      "sub/a.txt",
			isAdd:    true,
			expected: []string{`Attempting to Add file "sub/a.txt" that is excluded by .dockerignore`},
		},
		{
			name:     "file that is not excluded",
			patterns: []string{"sub"},
			src:      "other.txt",
		},

		// Negations that cannot re-include anything at or below the source
		// path must not disable the check for that source path.
		{
			name:     "negation for sibling file in the excluded directory",
			patterns: []string{"sub", "!sub/b.txt"},
			src:      "sub/a.txt",
			expected: []string{`Attempting to Copy file "sub/a.txt" that is excluded by .dockerignore`},
		},
		{
			name:     "negation for an unrelated directory",
			patterns: []string{"sub", "!other.txt"},
			src:      "sub/a.txt",
			expected: []string{`Attempting to Copy file "sub/a.txt" that is excluded by .dockerignore`},
		},
		{
			name:     "negation for a path that does not exist",
			patterns: []string{"sub", "!nope/missing.txt"},
			src:      "sub/a.txt",
			expected: []string{`Attempting to Copy file "sub/a.txt" that is excluded by .dockerignore`},
		},
		{
			name:     "negation before the exclusion",
			patterns: []string{"!sub/b.txt", "sub"},
			src:      "sub/a.txt",
			expected: []string{`Attempting to Copy file "sub/a.txt" that is excluded by .dockerignore`},
		},
		{
			name:     "negation for an unrelated directory with wildcard",
			patterns: []string{"sub", "!other/*.txt"},
			src:      "sub/a.txt",
			expected: []string{`Attempting to Copy file "sub/a.txt" that is excluded by .dockerignore`},
		},

		// Negations that re-include a path at or below the source path make
		// the exclusion impossible to determine statically, so the check has
		// to stay silent for that source path.
		{
			name:     "directory excluded and file inside it negated",
			patterns: []string{"sub", "!sub/keep.txt"},
			src:      "sub",
		},
		{
			name:     "directory excluded and nested file negated",
			patterns: []string{"sub", "!sub/nested/keep.txt"},
			src:      "sub",
		},
		{
			name:     "directory excluded and file inside it negated before the exclusion",
			patterns: []string{"!sub/keep.txt", "sub"},
			src:      "sub",
		},
		{
			name:     "directory excluded and wildcard negation inside it",
			patterns: []string{"sub", "!sub/*.txt"},
			src:      "sub",
		},
		{
			name:     "negation with a wildcard that may match the source path",
			patterns: []string{"sub", "!*/keep.txt"},
			src:      "sub",
		},
		{
			name:     "negated file is the source path",
			patterns: []string{"sub", "!sub/keep.txt"},
			src:      "sub/keep.txt",
		},
		{
			name:     "parent directory of the source path is negated",
			patterns: []string{"**", "!sub"},
			src:      "sub/a.txt",
		},

		// Context root handling.
		{
			name:     "context root with everything excluded",
			patterns: []string{"*"},
			src:      ".",
			expected: []string{`Attempting to Copy file "." that is excluded by .dockerignore`},
		},
		{
			name:     "context root with everything excluded and a negation",
			patterns: []string{"**", "!Dockerfile"},
			src:      ".",
		},
		{
			name:     "context root with only dotfiles excluded",
			patterns: []string{".*"},
			src:      ".",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			warnings := copySourceLintWarnings(t, tt.patterns, tt.src, tt.isAdd)
			require.Equal(t, tt.expected, warnings)
		})
	}
}
