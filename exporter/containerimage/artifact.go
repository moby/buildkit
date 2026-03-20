package containerimage

import (
	"strconv"
	"strings"

	intoto "github.com/in-toto/in-toto-golang/in_toto"
	"github.com/moby/buildkit/exporter"
	"github.com/moby/buildkit/exporter/attestation"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	"github.com/moby/buildkit/solver/result"
	"github.com/moby/buildkit/util/purl"
	digest "github.com/opencontainers/go-digest"
	"github.com/package-url/packageurl-go"
	"github.com/pkg/errors"
)

// ResolveArtifactAttestations selects the single attestation group that applies
// to an OCI artifact export. If forceInline is false, only inline-only
// attestations are retained, matching the regular image exporter behavior.
func ResolveArtifactAttestations(src *exporter.Source, forceInline bool) ([]exporter.Attestation, error) {
	if len(src.Attestations) == 0 {
		return nil, nil
	}

	attestationGroups := make(map[string][]exporter.Attestation, len(src.Attestations))
	for k, atts := range src.Attestations {
		if !forceInline {
			atts = attestation.Filter(atts, nil, map[string][]byte{
				result.AttestationInlineOnlyKey: []byte(strconv.FormatBool(true)),
			})
		}
		if len(atts) > 0 {
			attestationGroups[k] = atts
		}
	}
	if len(attestationGroups) == 0 {
		return nil, nil
	}
	if len(attestationGroups) == 1 {
		for _, atts := range attestationGroups {
			return atts, nil
		}
	}

	ps, err := exptypes.ParsePlatforms(src.Metadata)
	if err != nil {
		return nil, err
	}
	if len(ps.Platforms) != 1 {
		return nil, errors.Errorf("OCI artifact exports require exactly one attestation group, got %d", len(attestationGroups))
	}

	atts, ok := attestationGroups[ps.Platforms[0].ID]
	if !ok {
		return nil, errors.Errorf("OCI artifact export missing attestation group for %s", ps.Platforms[0].ID)
	}
	if len(attestationGroups) != 1 {
		return nil, errors.Errorf("OCI artifact exports require exactly one attestation group, got %d", len(attestationGroups))
	}
	return atts, nil
}

// DefaultArtifactSubjects creates default in-toto subjects for registry-pushed
// OCI artifacts. Artifact exports are platform-less, so no platform qualifier
// is added to the generated purls.
func DefaultArtifactSubjects(imageNames string, dgst digest.Digest) ([]intoto.Subject, error) {
	if imageNames == "" {
		return nil, nil
	}

	var subjects []intoto.Subject
	for name := range strings.SplitSeq(imageNames, ",") {
		if name == "" {
			continue
		}
		pl, err := purl.RefToPURL(packageurl.TypeDocker, name, nil)
		if err != nil {
			return nil, err
		}
		subjects = append(subjects, intoto.Subject{
			Name:   pl,
			Digest: result.ToDigestMap(dgst),
		})
	}
	return subjects, nil
}
