package containerblob

import (
	"testing"

	srctypes "github.com/moby/buildkit/source/types"
	"github.com/stretchr/testify/require"
)

func TestImageBlobIdentifierString(t *testing.T) {
	const ref = "docker.io/library/busybox@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	id, err := NewImageBlobIdentifier(ref, srctypes.DockerImageBlobScheme)
	require.NoError(t, err)
	require.Equal(t, "docker-image+blob://"+ref, id.String())
}

func TestOCIBlobIdentifierString(t *testing.T) {
	const ref = "example.com/repo/layout@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	id, err := NewImageBlobIdentifier(ref, srctypes.OCIBlobScheme)
	require.NoError(t, err)
	require.Equal(t, "oci-layout+blob://"+ref, id.String())
}
