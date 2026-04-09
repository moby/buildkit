package config

import "github.com/moby/buildkit/util/compression"

type RefConfig struct {
	Compression            compression.Config
	PreferNonDistributable bool
	// SkipParents skips recursive compression of parent layers. Used by the
	// eager export pipeline where each layer is compressed independently as
	// its vertex completes.
	SkipParents bool
}
