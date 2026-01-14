package exporter

import (
	"context"

	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	"github.com/moby/buildkit/solver/result"
	"github.com/moby/buildkit/util/compression"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
)

type Source = result.Result[cache.ImmutableRef]

type Attestation = result.Attestation[cache.ImmutableRef]

type Exporter interface {
	Resolve(context.Context, int, map[string]string) (ExporterInstance, error)
}

// FinalizeFunc completes an export operation after all exports have created
// their artifacts. It may perform network operations like pushing to a registry.
// FinalizeFunc is safe to call concurrently with other FinalizeFunc calls.
type FinalizeFunc func(ctx context.Context) error

// NoOpFinalize is a FinalizeFunc that does nothing. Use this for exporters
// that complete all work during Export (e.g., local, tar exporters).
var NoOpFinalize FinalizeFunc = func(context.Context) error { return nil }

type ExporterInstance interface {
	ID() int
	Name() string
	Config() *Config
	Type() string
	Attrs() map[string]string

	// Export performs the export operation and returns a finalize callback.
	//
	// The export is split into two phases to enable parallel execution:
	//   1. Export creates artifacts (layers, manifests) in the content store
	//   2. The returned FinalizeFunc pushes artifacts to the destination
	//
	// This split allows the solver to run cache export after image layers are
	// created in the content store, ensuring cache exporters can reuse existing
	// blobs rather than creating duplicates. The finalize callbacks for both
	// image and cache exports can then run concurrently.
	//
	// For exporters that don't push to a remote destination (tar, local),
	// all work is done in Export and NoOpFinalize should be returned.
	//
	// The caller must always call the returned FinalizeFunc exactly once,
	// even if it's NoOpFinalize.
	Export(ctx context.Context, src *Source, buildInfo ExportBuildInfo) (
		response map[string]string,
		finalize FinalizeFunc,
		ref DescriptorReference,
		err error,
	)
}

type ExportBuildInfo struct {
	Ref         string
	InlineCache exptypes.InlineCache
	SessionID   string
}

type DescriptorReference interface {
	Release() error
	Descriptor() ocispecs.Descriptor
}

type Config struct {
	// Make the field private in case it is initialized with nil compression.Type
	compression compression.Config
}

func NewConfig() *Config {
	return &Config{
		compression: compression.Config{
			Type: compression.Default,
		},
	}
}

func NewConfigWithCompression(comp compression.Config) *Config {
	return &Config{
		compression: comp,
	}
}

func (c *Config) Compression() compression.Config {
	return c.compression
}
