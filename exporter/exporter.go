package exporter

import (
	"context"

	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/executor"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	"github.com/moby/buildkit/frontend"
	"github.com/moby/buildkit/solver/result"
	"github.com/moby/buildkit/util/compression"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
)

type Source struct {
	*result.Result[cache.ImmutableRef]
	FrontendResult *frontend.Result
}

func (src *Source) Clone() *Source {
	if src == nil {
		return nil
	}
	return &Source{
		Result:         src.Result.Clone(),
		FrontendResult: src.FrontendResult.Clone(),
	}
}

type Attestation = result.Attestation[cache.ImmutableRef]

type Exporter interface {
	// XXX: resolver is so much more complicated now
	Resolve(ctx context.Context, id int, frontendAttrs map[string]string, exporterAttrs map[string]string, target exptypes.ExporterTarget) (ExporterInstance, error)
}

type ExporterInstance interface {
	ID() int
	Name() string
	Config() *Config
	Type() string
	Attrs() map[string]string
	Target() exptypes.ExporterTarget
	// XXX: so is export
	Export(ctx context.Context, llbBridge frontend.FrontendLLBBridge, exec executor.Executor, src *Source, inlineCache exptypes.InlineCache, sessionID string) (map[string]string, DescriptorReference, error)
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
