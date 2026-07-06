package local

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPlatformIDToPath(t *testing.T) {
	for _, tc := range []struct {
		name string
		id   string
		want string
	}{
		{
			name: "slashes",
			id:   "linux/amd64",
			want: "linux_amd64",
		},
		{
			name: "windows separators",
			id:   `..\buildkit-outside`,
			want: ".._buildkit-outside",
		},
		{
			name: "drive separator",
			id:   `windows/amd64:C`,
			want: "windows_amd64_C",
		},
		{
			name: "dot",
			id:   ".",
			want: "_",
		},
		{
			name: "dot dot",
			id:   "..",
			want: "__",
		},
		{
			name: "embedded dots",
			id:   "linux.v2/amd64",
			want: "linux.v2_amd64",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, PlatformIDToPath(tc.id))
		})
	}
}
