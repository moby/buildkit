package worker

import (
	"context"
	"io"

	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/cache/instructioncache"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/executor"
	"github.com/moby/buildkit/exporter"
	"github.com/moby/buildkit/solver/types"
	digest "github.com/opencontainers/go-digest"
)

type SubBuilder interface {
	SubBuild(ctx context.Context, dgst digest.Digest, req types.SolveRequest) (types.Ref, error)
}

type Worker interface {
	// ID needs to be unique in the cluster
	ID() string
	Labels() map[string]string
	InstructionCache() instructioncache.InstructionCache
	// ResolveOp resolves Vertex.Sys() to Op implementation. SubBuilder is needed for pb.Op_Build.
	ResolveOp(v types.Vertex, s SubBuilder) (types.Op, error)
	ResolveImageConfig(ctx context.Context, ref string) (digest.Digest, []byte, error)
	// Exec is similar to executor.Exec but without []mount.Mount
	Exec(ctx context.Context, meta executor.Meta, rootFS cache.ImmutableRef, stdin io.ReadCloser, stdout, stderr io.WriteCloser) error
	DiskUsage(ctx context.Context, opt client.DiskUsageInfo) ([]*client.UsageInfo, error)
	Exporter(name string) (exporter.Exporter, error)
	Prune(ctx context.Context, ch chan client.UsageInfo) error
}

// Pre-defined label keys
const (
	labelPrefix      = "org.mobyproject.buildkit.worker."
	LabelOS          = labelPrefix + "os"          // GOOS
	LabelArch        = labelPrefix + "arch"        // GOARCH
	LabelExecutor    = labelPrefix + "executor"    // "oci" or "containerd"
	LabelSnapshotter = labelPrefix + "snapshotter" // containerd snapshotter name ("overlay", "naive", ...)
	LabelHostname    = labelPrefix + "hostname"
)
