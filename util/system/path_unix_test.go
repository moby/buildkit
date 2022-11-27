//go:build !windows
// +build !windows

package system

import (
	"testing"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestNormalizeWorkdir(t *testing.T) {
	testCases := []struct {
		name           string
		currentWorkdir string
		newWorkDir     string
		desiredResult  string
		err            string
	}{
		{
			name:           "no current wd with relative wd",
			currentWorkdir: "",
			newWorkDir:     "test",
			desiredResult:  `/test`,
			err:            "",
		},
		{
			name:           "no current wd with absolute wd",
			currentWorkdir: "",
			newWorkDir:     `/strippedWd`,
			desiredResult:  `/strippedWd`,
			err:            "",
		},
		{
			name:           "current wd is absolute, new wd is relative",
			currentWorkdir: "/test",
			newWorkDir:     `subdir`,
			desiredResult:  `/test/subdir`,
			err:            "",
		},
		{
			name:           "current wd is absolute, new wd is relative one folder up",
			currentWorkdir: "/test",
			newWorkDir:     `../subdir`,
			desiredResult:  `/subdir`,
			err:            "",
		},
		{
			name:           "current wd is absolute, new wd is absolute",
			currentWorkdir: "/test",
			newWorkDir:     `/current`,
			desiredResult:  `/current`,
			err:            "",
		},
		{
			name:           "current wd is relative, new wd is relative",
			currentWorkdir: "test",
			newWorkDir:     `current`,
			desiredResult:  `/test/current`,
			err:            "",
		},
		{
			name:           "current wd is relative, no new wd",
			currentWorkdir: "test",
			newWorkDir:     "",
			desiredResult:  `/test`,
			err:            "",
		},
		{
			name:           "current wd is absolute, no new wd",
			currentWorkdir: "/test",
			newWorkDir:     "",
			desiredResult:  `/test`,
			err:            "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := NormalizeWorkdir(tc.currentWorkdir, tc.newWorkDir)
			if tc.err != "" {
				require.EqualError(t, errors.Cause(err), tc.err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tc.desiredResult, result)
		})
	}
}
