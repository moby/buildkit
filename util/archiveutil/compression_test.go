package archiveutil

import (
	"bytes"
	"compress/gzip"
	"io"
	"os/exec"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/require"
)

func TestDecompressStream(t *testing.T) {
	t.Parallel()

	gz := bytes.NewBuffer(nil)
	gzw := gzip.NewWriter(gz)
	_, err := gzw.Write([]byte("payload"))
	require.NoError(t, err)
	require.NoError(t, gzw.Close())

	zstdBuf := bytes.NewBuffer(nil)
	zstdw, err := zstd.NewWriter(zstdBuf)
	require.NoError(t, err)
	_, err = zstdw.Write([]byte("payload"))
	require.NoError(t, err)
	require.NoError(t, zstdw.Close())

	tests := []struct {
		name string
		dt   []byte
	}{
		{
			name: "uncompressed",
			dt:   []byte("payload"),
		},
		{
			name: "gzip",
			dt:   gz.Bytes(),
		},
		{
			name: "bzip2",
			dt: []byte{
				0x42, 0x5a, 0x68, 0x39, 0x31, 0x41, 0x59, 0x26,
				0x53, 0x59, 0x1d, 0x1b, 0x8a, 0x0d, 0x00, 0x00,
				0x02, 0x81, 0x80, 0x24, 0x04, 0xc0, 0x20, 0x20,
				0x00, 0x22, 0x18, 0x68, 0x30, 0x09, 0x58, 0x13,
				0x0b, 0xb9, 0x22, 0x9c, 0x28, 0x48, 0x0e, 0x8d,
				0xc5, 0x06, 0x80,
			},
		},
		{
			name: "zstd",
			dt:   zstdBuf.Bytes(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rc, err := DecompressStream(bytes.NewReader(tt.dt))
			require.NoError(t, err)
			defer rc.Close()

			dt, err := io.ReadAll(rc)
			require.NoError(t, err)
			require.Equal(t, "payload", string(dt))
		})
	}
}

func TestDecompressStreamXz(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("xz"); err != nil {
		t.Skip("xz binary is not available")
	}

	rc, err := DecompressStream(bytes.NewReader([]byte{
		0xfd, 0x37, 0x7a, 0x58, 0x5a, 0x00, 0x00, 0x04,
		0xe6, 0xd6, 0xb4, 0x46, 0x02, 0x00, 0x21, 0x01,
		0x16, 0x00, 0x00, 0x00, 0x74, 0x2f, 0xe5, 0xa3,
		0x01, 0x00, 0x06, 0x70, 0x61, 0x79, 0x6c, 0x6f,
		0x61, 0x64, 0x00, 0x00, 0x8b, 0x1f, 0xd0, 0x83,
		0x9e, 0x49, 0x8f, 0x37, 0x00, 0x01, 0x1f, 0x07,
		0x16, 0x2e, 0xb8, 0x73, 0x1f, 0xb6, 0xf3, 0x7d,
		0x01, 0x00, 0x00, 0x00, 0x00, 0x04, 0x59, 0x5a,
	}))
	require.NoError(t, err)
	defer rc.Close()

	dt, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.Equal(t, "payload", string(dt))
}
