package exporter

import (
	"context"

	"github.com/moby/buildkit/cache"
	controlapi "github.com/moby/buildkit/api/services/control"
)

type Exporter interface {
	Resolve(context.Context, map[string]string) (ExporterInstance, error)
}

type ExporterInstance interface {
	Name() string
	Export(context.Context, Source) (*controlapi.ExporterResponse, error)
}

type Source struct {
	Ref      cache.ImmutableRef
	Refs     map[string]cache.ImmutableRef
	Metadata map[string][]byte
}
