package sbom

import (
	"testing"

	intoto "github.com/in-toto/in-toto-golang/in_toto"
	"github.com/moby/buildkit/solver/result"
	"github.com/stretchr/testify/assert"
)

func TestIsSBOMPredicateType(t *testing.T) {
	assert.True(t, IsSBOMPredicateType(intoto.PredicateSPDX))
	assert.True(t, IsSBOMPredicateType(intoto.PredicateCycloneDX))
	assert.False(t, IsSBOMPredicateType(""))
	assert.False(t, IsSBOMPredicateType("https://slsa.dev/provenance/v0.2"))
	assert.False(t, IsSBOMPredicateType("https://example.com/unknown"))
}

func TestHasSBOM(t *testing.T) {
	t.Run("SPDX", func(t *testing.T) {
		res := &result.Result[int]{}
		res.Attestations = map[string][]result.Attestation[int]{
			"linux/amd64": {
				{InToto: result.InTotoAttestation{PredicateType: intoto.PredicateSPDX}},
			},
		}
		assert.True(t, HasSBOM(res))
	})

	t.Run("CycloneDX", func(t *testing.T) {
		res := &result.Result[int]{}
		res.Attestations = map[string][]result.Attestation[int]{
			"linux/amd64": {
				{InToto: result.InTotoAttestation{PredicateType: intoto.PredicateCycloneDX}},
			},
		}
		assert.True(t, HasSBOM(res))
	})

	t.Run("none", func(t *testing.T) {
		res := &result.Result[int]{}
		res.Attestations = map[string][]result.Attestation[int]{
			"linux/amd64": {
				{InToto: result.InTotoAttestation{PredicateType: "https://slsa.dev/provenance/v0.2"}},
			},
		}
		assert.False(t, HasSBOM(res))
	})

	t.Run("empty", func(t *testing.T) {
		res := &result.Result[int]{}
		assert.False(t, HasSBOM(res))
	})
}
