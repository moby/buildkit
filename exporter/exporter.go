package exporter

import (
	"context"

	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/util/attestation"
	"github.com/moby/buildkit/util/compression"
)

type Exporter interface {
	Resolve(context.Context, map[string]string) (ExporterInstance, error)
}

type ExporterInstance interface {
	Name() string
	Config() Config
	Export(ctx context.Context, src Source, sessionID string) (map[string]string, error)
}

type Config struct {
	Compression compression.Config
}

type Source struct {
	Ref          cache.ImmutableRef
	Refs         map[string]cache.ImmutableRef
	Metadata     map[string][]byte
	Attestations map[string][]Attestation
}

type Attestation interface {
	isExporterAttestation()
}

type InTotoAttestation struct {
	PredicateType string
	PredicateRef  cache.ImmutableRef
	PredicatePath string
	Subjects      []attestation.InTotoSubject
}

func (a *InTotoAttestation) isExporterAttestation() {}
