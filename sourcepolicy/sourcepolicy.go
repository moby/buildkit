// Package sourcepolicy provides the source policy types
package sourcepolicy

import binfotypes "github.com/moby/buildkit/util/buildinfo/types"

// SourceType defines the source type.
type SourceType = binfotypes.SourceType

// Source is similar to github.com/moby/buildkit/util/buildinfo/types.Source .
type Source struct {
	// Type defines the SourceType source type (docker-image, git, http).
	Type SourceType `json:"type,omitempty"`
	// Ref is the reference of the source.
	Ref string `json:"ref,omitempty"`
	// Pin is the source digest.
	Pin string `json:"pin,omitempty"`
}

// SourcePolicy provides pinning information to ensure determinism of sources.
type SourcePolicy struct {
	// Sources do not need to cover all the sources.
	// Sources may contain unreferenced sources too.
	Sources []Source `json:"sources,omitempty"`
}
