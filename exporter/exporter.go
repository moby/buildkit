package exporter

import (
	"context"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/remotes/docker"
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

type ExporterInstance interface {
	ID() int
	Name() string
	Config() *Config
	Type() string
	Attrs() map[string]string
	Export(ctx context.Context, src *Source, inlineCache exptypes.InlineCache, sessionID string) (map[string]string, DescriptorReference, error)
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

// EagerPushConfig holds the registry details needed to push individual layer
// blobs during the build, before the final manifest is assembled.
type EagerPushConfig struct {
	TargetName         string
	RegistryHosts      docker.RegistryHosts
	Insecure           bool
	ContentStore       content.Store
	PreferPushRegistry bool
}

// EagerExportProvider is an optional interface that ExporterInstances can
// implement to supply configuration for the eager export pipeline.
type EagerExportProvider interface {
	EagerPushConfig() *EagerPushConfig
}
