package snapshot

import (
	"strings"
	"testing"

	digest "github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
)

func TestLayerSnapshotID(t *testing.T) {
	for _, algorithm := range []digest.Algorithm{digest.SHA256, digest.SHA384, digest.SHA512} {
		t.Run(algorithm.String(), func(t *testing.T) {
			chainID := algorithm.FromString("layer")
			id := LayerSnapshotID(chainID)
			require.Equal(t, "buildkit-layer-snapshot-v1-"+algorithm.String()+"-"+chainID.Encoded(), id)

			parsed, ok := ParseLayerSnapshotID(id)
			require.True(t, ok)
			require.Equal(t, chainID, parsed)
		})
	}
}

func TestParseLayerSnapshotIDRejectsInvalidID(t *testing.T) {
	chainID := digest.FromString("layer")
	for _, id := range []string{
		"",
		chainID.String(),
		"buildkit-layer-snapshot-v1-",
		"buildkit-layer-snapshot-v1-sha256",
		"buildkit-layer-snapshot-v1-sha256-",
		"buildkit-layer-snapshot-v1-sha256-invalid",
		"buildkit-layer-snapshot-v1-sha256-" + strings.ToUpper(chainID.Encoded()),
		"buildkit-layer-snapshot-v1-sha256-" + chainID.Encoded() + "-view",
		"buildkit-layer-snapshot-v1-md5-d41d8cd98f00b204e9800998ecf8427e",
		"buildkit-layer-snapshot-v2-sha256-" + chainID.Encoded(),
	} {
		_, ok := ParseLayerSnapshotID(id)
		require.False(t, ok, id)
	}
}
