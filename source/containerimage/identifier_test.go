package containerimage

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestImageIdentifierString(t *testing.T) {
	id, err := NewImageIdentifier("docker.io/library/busybox:latest")
	require.NoError(t, err)
	require.Equal(t, "docker-image://docker.io/library/busybox:latest", id.String())
}

func TestOCIIdentifierString(t *testing.T) {
	id, err := NewOCIIdentifier("example.com/repo/layout:latest")
	require.NoError(t, err)
	require.Equal(t, "oci-layout://example.com/repo/layout:latest", id.String())
}
