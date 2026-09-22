package compression

import (
	"bytes"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDetectCompressionType(t *testing.T) {
	for _, tc := range []struct {
		name   string
		header []byte
		want   Type
	}{
		{name: "empty", want: Uncompressed},
		{name: "uncompressed", header: []byte("payload"), want: Uncompressed},
		{name: "gzip", header: []byte{0x1F, 0x8B, 0x08}, want: Gzip},
		{name: "zstd", header: []byte{0x28, 0xB5, 0x2F, 0xFD}, want: Zstd},
		{name: "zstd skippable frame", header: []byte{0x50, 0x2A, 0x4D, 0x18, 0, 0, 0, 0}, want: Zstd},
		{name: "zstd skippable frame range end", header: []byte{0x5F, 0x2A, 0x4D, 0x18, 0, 0, 0, 0}, want: Zstd},
		{name: "short zstd prefix", header: []byte{0x28, 0xB5, 0x2F}, want: Uncompressed},
		{name: "short skippable frame", header: []byte{0x50, 0x2A, 0x4D, 0x18}, want: Uncompressed},
		{name: "outside skippable range", header: []byte{0x60, 0x2A, 0x4D, 0x18, 0, 0, 0, 0}, want: Uncompressed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := bytes.NewReader(tc.header)
			got, err := detectCompressionType(io.NewSectionReader(r, 0, r.Size()))
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}
