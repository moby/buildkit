package dockerui

import (
	"archive/tar"
	"bytes"
	"testing"

	"github.com/containerd/platforms"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

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
			require.Equal(t, tc.expected, isArchive(tc.header))
		})
	}
}

func TestNormalizePlatform(t *testing.T) {
	testCases := []struct {
		p, imgP  ocispecs.Platform
		expected exptypes.Platform
	}{
		{
			p: ocispecs.Platform{
				Architecture: "arm64",
				OS:           "linux",
				Variant:      "v8",
			},
			imgP: ocispecs.Platform{
				Architecture: "arm64",
				OS:           "linux",
			},
			expected: exptypes.Platform{
				ID: "linux/arm64", // Not "linux/arm64/v8" https://github.com/moby/buildkit/issues/5915
				Platform: ocispecs.Platform{
					Architecture: "arm64",
					OS:           "linux",
				},
			},
		},
		{
			p: ocispecs.Platform{
				Architecture: "arm64",
				OS:           "linux",
				Variant:      "v8",
			},
			imgP: ocispecs.Platform{
				Architecture: "arm64",
				OS:           "linux",
				Variant:      "v8",
			},
			expected: exptypes.Platform{
				ID: "linux/arm64",
				Platform: ocispecs.Platform{
					Architecture: "arm64",
					OS:           "linux",
				},
			},
		},
		{
			p: ocispecs.Platform{
				Architecture: "amd64",
				OS:           "windows",
			},
			imgP: ocispecs.Platform{
				Architecture: "amd64",
				OS:           "windows",
				OSVersion:    "10.0.19041.0",
			},
			expected: exptypes.Platform{
				ID: "windows/amd64",
				Platform: ocispecs.Platform{
					Architecture: "amd64",
					OS:           "windows",
					OSVersion:    "10.0.19041.0",
				},
			},
		},
		{
			p: ocispecs.Platform{
				Architecture: "amd64",
				OS:           "windows",
				OSVersion:    "10.0.19041.0",
			},
			imgP: ocispecs.Platform{
				Architecture: "amd64",
				OS:           "windows",
				OSVersion:    "11.0.22000.0",
			},
			expected: exptypes.Platform{
				ID: "windows(10.0.19041.0)/amd64",
				Platform: ocispecs.Platform{
					Architecture: "amd64",
					OS:           "windows",
					OSVersion:    "10.0.19041.0",
				},
			},
		},
	}

	for _, tc := range testCases {
		require.Equal(t, tc.expected, makeExportPlatform(tc.p, tc.imgP))
		// the ID needs to always be formatall(normalize(p))
		require.Equal(t, platforms.FormatAll(platforms.Normalize(tc.p)), tc.expected.ID)
	}
}
