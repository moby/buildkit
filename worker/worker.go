package worker

import (
	"io"

	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/cache/instructioncache"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/executor"
	"github.com/moby/buildkit/exporter"
	"github.com/moby/buildkit/solver/types"
	digest "github.com/opencontainers/go-digest"
	"golang.org/x/net/context"
)

// Worker is a local worker instance with dedicated snapshotter, cache, and so on.
// TODO: s/Worker/OpWorker/g ?
// FIXME: Worker should be rather an interface
// type Worker struct {
// 	WorkerOpt
// 	Snapshotter      ctdsnapshot.Snapshotter // blobmapping snapshotter
// 	CacheManager     cache.Manager
// 	SourceManager    *source.Manager
// 	InstructionCache instructioncache.InstructionCache
// 	Exporters        map[string]exporter.Exporter
// 	ImageSource      source.Source
// 	CacheExporter    *cacheimport.CacheExporter
// 	CacheImporter    *cacheimport.CacheImporter
// 	// no frontend here
// }

type SubBuilder interface {
	SubBuild(ctx context.Context, dgst digest.Digest, req types.SolveRequest) (types.Ref, error)
}

type Worker interface {
	InstructionCache() instructioncache.InstructionCache
	Resolve(v types.Vertex, s SubBuilder) (types.Op, error)
	ResolveImageConfig(ctx context.Context, ref string) (digest.Digest, []byte, error)
	Exec(ctx context.Context, meta executor.Meta, rootFS cache.ImmutableRef, stdin io.ReadCloser, stdout, stderr io.WriteCloser) error
	DiskUsage(ctx context.Context, opt client.DiskUsageInfo) ([]*client.UsageInfo, error)
	Name() string
	Exporter(name string) (exporter.Exporter, error)
}
