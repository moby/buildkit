package compression

import (
	"archive/tar"
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

func TestIsArchive(t *testing.T) {
	var tarBuf bytes.Buffer
	tw := tar.NewWriter(&tarBuf)
	err := tw.WriteHeader(&tar.Header{
		Name: "file",
		Mode: 0o644,
		Size: 4,
	})
	require.NoError(t, err)
	_, err = tw.Write([]byte("test"))
	require.NoError(t, err)
	require.NoError(t, tw.Close())

	testCases := []struct {
		name     string
		header   []byte
		expected bool
	}{
		{name: "bzip2", header: []byte{0x42, 0x5A, 0x68}, expected: true},
		{name: "gzip", header: []byte{0x1F, 0x8B, 0x08}, expected: true},
		{name: "xz", header: []byte{0xFD, 0x37, 0x7A, 0x58, 0x5A, 0x00}, expected: true},
		{name: "zstd", header: []byte{0x28, 0xB5, 0x2F, 0xFD}, expected: true},
		{name: "zstd skippable frame", header: []byte{0x50, 0x2A, 0x4D, 0x18, 0x00, 0x00, 0x00, 0x00}, expected: true},
		{name: "zstd skippable frame range end", header: []byte{0x5F, 0x2A, 0x4D, 0x18, 0x00, 0x00, 0x00, 0x00}, expected: true},
		{name: "tar", header: tarBuf.Bytes(), expected: true},
		{name: "unknown", header: []byte("not an archive"), expected: false},
		{name: "short zstd prefix", header: []byte{0x28, 0xB5, 0x2F}, expected: false},
		{name: "short zstd skippable frame", header: []byte{0x50, 0x2A, 0x4D, 0x18}, expected: false},
		{name: "outside zstd skippable frame range", header: []byte{0x60, 0x2A, 0x4D, 0x18, 0x00, 0x00, 0x00, 0x00}, expected: false},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.expected, IsArchive(tc.header))
		})
	}
}
