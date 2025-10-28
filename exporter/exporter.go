package exporter

import (
	"context"
	"io"

	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/solver/result"
	"github.com/moby/buildkit/util/compression"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/tonistiigi/fsutil"
)

type Source = result.Result[cache.ImmutableRef]

type Attestation = result.Attestation[cache.ImmutableRef]

type Exporter interface {
	Resolve(context.Context, map[string]string) (ExporterInstance, error)
}

type ExporterInstance interface {
	Name() string
	Config() *Config
	Type() string
	Attrs() map[string]string
	Export(ctx context.Context, src *Source, inlineCache exptypes.InlineCache, sessionID string, apis ExporterAPIs) (map[string]string, DescriptorReference, error)
}

type ExporterAPIs struct {
	CopyFileWriter func(_ context.Context, md map[string]string, c session.Caller) (io.WriteCloser, error)
	CopyToCaller   func(_ context.Context, fs fsutil.FS, c session.Caller, progress func(int, bool)) error
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
