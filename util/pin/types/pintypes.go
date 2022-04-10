// Package pintypes provides pin types
package pintypes

import binfotypes "github.com/moby/buildkit/util/buildinfo/types"

// Source corresponds to buildinfo Source.
type Source = binfotypes.Source

// Pin provides pinning information to ensure determinism of sources.
type Pin struct {
	// Sources do not need to cover all the sources.
	// Sources may contain unreferenced sources too.
	Sources []Source `json:"sources,omitempty"`
}

// Consumed defines the consumed pins
type Consumed = Pin
