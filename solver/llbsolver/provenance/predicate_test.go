package provenance

import (
	"testing"

	provenancetypes "github.com/moby/buildkit/solver/llbsolver/provenance/types"
	digest "github.com/opencontainers/go-digest"
	packageurl "github.com/package-url/packageurl-go"
	"github.com/stretchr/testify/require"
)

func TestSLSAMaterialsImageBlobPURL(t *testing.T) {
	t.Parallel()

	dgst := digest.FromString("blobdata")
	ms, err := slsaMaterials(provenancetypes.Sources{
		ImageBlobs: []provenancetypes.ImageBlobSource{
			{
				Ref:    "example.com/ns/repo@" + dgst.String(),
				Digest: dgst,
			},
		},
	})
	require.NoError(t, err)
	require.Len(t, ms, 1)

	p, err := packageurl.FromString(ms[0].URI)
	require.NoError(t, err)
	require.Equal(t, packageurl.TypeDocker, p.Type)
	require.Equal(t, "example.com/ns", p.Namespace)
	require.Equal(t, "repo", p.Name)
	require.Equal(t, "", p.Version)

	q := p.Qualifiers.Map()
	require.Equal(t, "blob", q["ref_type"])
	require.Equal(t, dgst.String(), q["digest"])

	require.Equal(t, dgst.Hex(), ms[0].Digest[dgst.Algorithm().String()])
}
