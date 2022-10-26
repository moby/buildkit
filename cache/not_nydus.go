//go:build !nydus
// +build !nydus

package cache

import (
	"context"

	"github.com/containerd/containerd/content"
	"github.com/moby/buildkit/cache/config"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/util/compression"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
)

var validAnnotations = append(compression.EStargzAnnotations, containerdUncompressed)

func needsForceCompression(ctx context.Context, cs content.Store, source ocispecs.Descriptor, refCfg config.RefConfig) bool {
	return refCfg.Compression.Force
}

func PatchLayers(ctx context.Context, ref ImmutableRef, refCfg config.RefConfig, remote *solver.Remote, sg session.Group) (*solver.Remote, error) {
	return remote, nil
}
