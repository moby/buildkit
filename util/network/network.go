package network

import (
	"context"
	"io"

	resourcestypes "github.com/moby/buildkit/executor/resources/types"
	specs "github.com/opencontainers/runtime-spec/specs-go"
)

// Provider interface for Network
type Provider interface {
	io.Closer
	New(ctx context.Context, hostname string, opt NamespaceOptions) (Namespace, error)
}

type NamespaceOptions struct {
	ProxyPolicy  ProxyPolicy
	ProxyCapture *ProxyCapture
}

// Namespace of network for workers
type Namespace interface {
	io.Closer
	// Set the namespace on the spec
	Set(*specs.Spec) error

	Sample() (*resourcestypes.NetworkSample, error)
}
