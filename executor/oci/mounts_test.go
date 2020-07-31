package oci

import (
	"runtime"
	"testing"

	"github.com/containerd/containerd/oci"
	"github.com/moby/buildkit/util/appcontext"
	specs "github.com/opencontainers/runtime-spec/specs-go"
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

func TestWithRemovedMounts(t *testing.T) {
	// The default mount-list from containerd
	s := oci.Spec{
		Mounts: []specs.Mount{
			{
				Destination: "/proc",
				Type:        "proc",
				Source:      "proc",
				Options:     []string{"nosuid", "noexec", "nodev"},
			},
			{
				Destination: "/dev",
				Type:        "tmpfs",
				Source:      "tmpfs",
				Options:     []string{"nosuid", "strictatime", "mode=755", "size=65536k"},
			},
			{
				Destination: "/dev/pts",
				Type:        "devpts",
				Source:      "devpts",
				Options:     []string{"nosuid", "noexec", "newinstance", "ptmxmode=0666", "mode=0620", "gid=5"},
			},
			{
				Destination: "/dev/shm",
				Type:        "tmpfs",
				Source:      "shm",
				Options:     []string{"nosuid", "noexec", "nodev", "mode=1777", "size=65536k"},
			},
			{
				Destination: "/dev/mqueue",
				Type:        "mqueue",
				Source:      "mqueue",
				Options:     []string{"nosuid", "noexec", "nodev"},
			},
			{
				Destination: "/sys",
				Type:        "sysfs",
				Source:      "sysfs",
				Options:     []string{"nosuid", "noexec", "nodev", "ro"},
			},
			{
				Destination: "/run",
				Type:        "tmpfs",
				Source:      "tmpfs",
				Options:     []string{"nosuid", "strictatime", "mode=755", "size=65536k"},
			},
		},
	}

	oldLen := len(s.Mounts)
	err := withRemovedMount("/run")(appcontext.Context(), nil, nil, &s)
	assert.NoError(t, err)
	assert.Equal(t, oldLen-1, len(s.Mounts))
}
