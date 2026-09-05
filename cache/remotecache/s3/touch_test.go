package s3

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestBuildCopySourceRange checks the ranges used to refresh an object larger
// than the CopyObject limit. A copy source range is inclusive, so the last
// byte it may name is objectSize-1.
func TestBuildCopySourceRange(t *testing.T) {
	tests := []struct {
		name       string
		start      int64
		objectSize int64
		want       string
	}{
		{
			name:       "first part of a larger object",
			start:      0,
			objectSize: 3 * maxCopyObjectSize,
			want:       "bytes=0-5368709119",
		},
		{
			name:       "final short part",
			start:      maxCopyObjectSize,
			objectSize: maxCopyObjectSize + 100,
			want:       "bytes=5368709120-5368709219",
		},
		{
			name:       "remainder ends exactly on the part size",
			start:      0,
			objectSize: maxCopyObjectSize,
			want:       "bytes=0-5368709119",
		},
		{
			name:       "remainder one byte short of the part size",
			start:      0,
			objectSize: maxCopyObjectSize - 1,
			want:       "bytes=0-5368709118",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildCopySourceRange(tt.start, tt.objectSize)
			require.Equal(t, tt.want, got)

			// The range must never address a byte past the end of the object.
			var start, end int64
			_, err := fmt.Sscanf(got, "bytes=%d-%d", &start, &end)
			require.NoError(t, err)
			require.Less(t, end, tt.objectSize)
			require.LessOrEqual(t, start, end)
		})
	}
}
