package oci

import (
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHasPrefix(t *testing.T) {
	type testCase struct {
		path     string
		prefix   string
		expected bool
	}
	testCases := []testCase{
		{
			path:     "/foo/bar",
			prefix:   "/foo",
			expected: true,
		},
		{
			path:     "/foo/bar",
			prefix:   "/foo/",
			expected: true,
		},
		{
			path:     "/foo/bar",
			prefix:   "/",
			expected: true,
		},
		{
			path:     "/foo",
			prefix:   "/foo",
			expected: true,
		},
		{
			path:     "/foo/bar",
			prefix:   "/bar",
			expected: false,
		},
		{
			path:     "/foo/bar",
			prefix:   "foo",
			expected: false,
		},
		{
			path:     "/foobar",
			prefix:   "/foo",
			expected: false,
		},
	}
	if runtime.GOOS == "windows" {
		testCases = append(testCases,
			testCase{
				path:     "C:\\foo\\bar",
				prefix:   "C:\\foo",
				expected: true,
			},
			testCase{
				path:     "C:\\foo\\bar",
				prefix:   "C:\\foo\\",
				expected: true,
			},
			testCase{
				path:     "C:\\foo\\bar",
				prefix:   "C:\\",
				expected: true,
			},
			testCase{
				path:     "C:\\foo",
				prefix:   "C:\\foo",
				expected: true,
			},
			testCase{
				path:     "C:\\foo\\bar",
				prefix:   "C:\\bar",
				expected: false,
			},
			testCase{
				path:     "C:\\foo\\bar",
				prefix:   "foo",
				expected: false,
			},
			testCase{
				path:     "C:\\foobar",
				prefix:   "C:\\foo",
				expected: false,
			},
		)
	}
	for i, tc := range testCases {
		actual := hasPrefix(tc.path, tc.prefix)
		assert.Equal(t, tc.expected, actual, "#%d: under(%q,%q)", i, tc.path, tc.prefix)
	}
}
