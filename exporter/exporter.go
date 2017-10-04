package exporter

import (
	"github.com/moby/buildkit/cache"
	"golang.org/x/net/context"
)

type Exporter interface {
	Resolve(context.Context, map[string]string) (ExporterInstance, error)
}

type ExporterInstance interface {
	Name() string
	Export(context.Context, cache.ImmutableRef, map[string][]byte) error
}
