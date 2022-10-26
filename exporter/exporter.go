package exporter

import (
	"context"

	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/solver/result"
	"github.com/moby/buildkit/util/compression"
)

type Source = result.Result[cache.ImmutableRef]

type Exporter interface {
	Resolve(context.Context, map[string]string) (ExporterInstance, error)
}

type ExporterInstance interface {
	Name() string
	Config() Config
	Export(ctx context.Context, src *Source, sessionID string) (map[string]string, error)
}

type Config struct {
	Compression compression.Config
}
