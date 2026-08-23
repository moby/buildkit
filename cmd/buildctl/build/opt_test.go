package build

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseOpt(t *testing.T) {
	type testCase struct {
		name        string
		opts        []string // --opt
		env         map[string]string
		expected    map[string]string
		expectedErr string
	}
	testCases := []testCase{
		{
			name:     "values are taken as given",
			opts:     []string{"target=foo", "build-arg:FOO=bar"},
			expected: map[string]string{"target": "foo", "build-arg:FOO": "bar"},
		},
		{
			name:     "a build arg without a value is read from the environment",
			opts:     []string{"build-arg:IMAGE"},
			env:      map[string]string{"IMAGE": "alpine"},
			expected: map[string]string{"build-arg:IMAGE": "alpine"},
		},
		{
			name:     "a build arg missing from the environment is dropped",
			opts:     []string{"build-arg:IMAGE"},
			expected: map[string]string{},
		},
		{
			name:     "an empty environment variable is kept",
			opts:     []string{"build-arg:IMAGE"},
			env:      map[string]string{"IMAGE": ""},
			expected: map[string]string{"build-arg:IMAGE": ""},
		},
		{
			name:     "an explicit value wins over the environment",
			opts:     []string{"build-arg:IMAGE=busybox"},
			env:      map[string]string{"IMAGE": "alpine"},
			expected: map[string]string{"build-arg:IMAGE": "busybox"},
		},
		{
			name:     "an explicit empty value is kept",
			opts:     []string{"build-arg:IMAGE="},
			env:      map[string]string{"IMAGE": "alpine"},
			expected: map[string]string{"build-arg:IMAGE": ""},
		},
		{
			name:     "the last of two build args wins, valueless last",
			opts:     []string{"build-arg:IMAGE=busybox", "build-arg:IMAGE"},
			env:      map[string]string{"IMAGE": "alpine"},
			expected: map[string]string{"build-arg:IMAGE": "alpine"},
		},
		{
			name:     "the last of two build args wins, valueless first",
			opts:     []string{"build-arg:IMAGE", "build-arg:IMAGE=busybox"},
			env:      map[string]string{"IMAGE": "alpine"},
			expected: map[string]string{"build-arg:IMAGE": "busybox"},
		},
		{
			name:     "a valueless build arg missing from the environment does not clear an earlier value",
			opts:     []string{"build-arg:IMAGE=busybox", "build-arg:IMAGE"},
			expected: map[string]string{"build-arg:IMAGE": "busybox"},
		},
		{
			name:        "other options still require a value",
			opts:        []string{"target"},
			expectedErr: "invalid value target",
		},
		{
			name:        "a build arg with no name still requires a value",
			opts:        []string{"build-arg:"},
			expectedErr: "invalid value build-arg:",
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// SOURCE_DATE_EPOCH is propagated by loadOptEnv, and the cases
			// below expect IMAGE to be unset unless the case sets it. Clear
			// both so an ambient value cannot leak into the expectations.
			for _, k := range []string{"SOURCE_DATE_EPOCH", "IMAGE"} {
				if v, ok := os.LookupEnv(k); ok {
					require.NoError(t, os.Unsetenv(k))
					t.Cleanup(func() { os.Setenv(k, v) }) //nolint:usetesting // cannot use t.Setenv for unsetting env-vars.
				}
			}
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			opt, err := ParseOpt(tc.opts)
			if tc.expectedErr != "" {
				require.EqualError(t, err, tc.expectedErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.expected, opt)
		})
	}
}
