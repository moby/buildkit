package provenance

import (
	"encoding/hex"
	"strings"

	distreference "github.com/docker/distribution/reference"
	slsa "github.com/in-toto/in-toto-golang/in_toto/slsa_provenance/v0.2"
	binfotypes "github.com/moby/buildkit/util/buildinfo/types"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

var BuildKitBuildType = "https://mobyproject.org/buildkit@v1"

type ProvenancePredicate struct {
	slsa.ProvenancePredicate
	Metadata *ProvenanceMetadata                `json:"metadata,omitempty"`
	Source   *Source                            `json:"buildSource,omitempty"`
	Layers   map[string][][]ocispecs.Descriptor `json:"buildLayers,omitempty"`
}

type ProvenanceMetadata struct {
	slsa.ProvenanceMetadata
	VCS map[string]string `json:"vcs,omitempty"`
}

func convertMaterial(s binfotypes.Source) (*slsa.ProvenanceMaterial, error) {
	// https://github.com/package-url/purl-spec/blob/master/PURL-TYPES.rst
	switch s.Type {
	case binfotypes.SourceTypeDockerImage:
		dgst, err := digest.Parse(s.Pin)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to parse digest %q for %s", s.Pin, s.Ref)
		}
		named, err := distreference.ParseNamed(s.Ref)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to parse ref %q", s.Ref)
		}
		version := ""
		if tagged, ok := named.(distreference.Tagged); ok {
			version = tagged.Tag()
		} else {
			if canonical, ok := named.(distreference.Canonical); ok {
				version = canonical.Digest().String()
			}
		}
		uri := "pkg:docker/" + distreference.FamiliarName(named)
		if version != "" {
			uri += "@" + version
		}
		return &slsa.ProvenanceMaterial{
			URI: uri,
			Digest: slsa.DigestSet{
				dgst.Algorithm().String(): dgst.Hex(),
			},
		}, nil
	case binfotypes.SourceTypeGit:
		if _, err := hex.DecodeString(s.Pin); err != nil {
			return nil, errors.Wrapf(err, "failed to parse commit %q for %s", s.Pin, s.Ref)
		}
		return &slsa.ProvenanceMaterial{
			URI: s.Ref,
			Digest: slsa.DigestSet{
				"sha1": s.Pin, // TODO: check length?
			},
		}, nil
	case binfotypes.SourceTypeHTTP:
		dgst, err := digest.Parse(s.Pin)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to parse digest %q for %s", s.Pin, s.Ref)
		}
		return &slsa.ProvenanceMaterial{
			URI: s.Ref,
			Digest: slsa.DigestSet{
				dgst.Algorithm().String(): dgst.Hex(),
			},
		}, nil
	default:
		return nil, errors.Errorf("unsupported source type %q", s.Type)
	}
}

func findMaterial(srcs []binfotypes.Source, uri string) (*slsa.ProvenanceMaterial, bool) {
	for _, s := range srcs {
		if s.Ref == uri {
			m, err := convertMaterial(s)
			if err != nil {
				continue
			}
			return m, true
		}
	}
	return nil, false
}

func FromBuildInfo(bi binfotypes.BuildInfo) (*ProvenancePredicate, error) {
	materials := make([]slsa.ProvenanceMaterial, len(bi.Sources))
	for i, s := range bi.Sources {
		m, err := convertMaterial(s)
		if err != nil {
			return nil, err
		}
		materials[i] = *m
	}

	inv := slsa.ProvenanceInvocation{}

	contextKey := "context"
	if v, ok := bi.Attrs["contextkey"]; ok && v != nil {
		contextKey = *v
	}

	if v, ok := bi.Attrs[contextKey]; ok && v != nil {
		if m, ok := findMaterial(bi.Sources, *v); ok {
			inv.ConfigSource.URI = m.URI
			inv.ConfigSource.Digest = m.Digest
		} else {
			inv.ConfigSource.URI = *v
		}
		delete(bi.Attrs, contextKey)
	}

	if v, ok := bi.Attrs["filename"]; ok && v != nil {
		inv.ConfigSource.EntryPoint = *v
		delete(bi.Attrs, "filename")
	}

	vcs := make(map[string]string)
	for k, v := range bi.Attrs {
		if strings.HasPrefix(k, "vcs:") {
			delete(bi.Attrs, k)
			if v != nil {
				vcs[strings.TrimPrefix(k, "vcs:")] = *v
			}
		}
	}

	inv.Parameters = bi.Attrs

	pr := &ProvenancePredicate{
		ProvenancePredicate: slsa.ProvenancePredicate{
			BuildType:  BuildKitBuildType,
			Invocation: inv,
			Materials:  materials,
		},
		Metadata: &ProvenanceMetadata{
			ProvenanceMetadata: slsa.ProvenanceMetadata{
				Completeness: slsa.ProvenanceComplete{
					Parameters:  true,
					Environment: true,
					Materials:   true, // TODO: check that there were no local sources
				},
			},
		},
	}

	if len(vcs) > 0 {
		pr.Metadata.VCS = vcs
	}

	return pr, nil
}
