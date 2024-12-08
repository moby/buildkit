package llbsolver

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
	"github.com/tonistiigi/fsutil"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc/codes"

	controlapi "github.com/moby/buildkit/api/services/control"
	"github.com/moby/buildkit/cache"
	cacheconfig "github.com/moby/buildkit/cache/config"
	"github.com/moby/buildkit/cache/remotecache"
	"github.com/moby/buildkit/exporter"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	"github.com/moby/buildkit/exporter/verifier"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/session"
	sessionexporter "github.com/moby/buildkit/session/exporter"
	"github.com/moby/buildkit/session/filesync"
	"github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/solver/result"
	"github.com/moby/buildkit/util/compression"
	"github.com/moby/buildkit/util/grpcerrors"
	"github.com/moby/buildkit/util/progress"
	"github.com/moby/buildkit/util/tracing"
	"github.com/moby/buildkit/worker"
)

func (s *Solver) getSessionExporters(ctx context.Context, sessionID string, id int, inp *exporter.Source) ([]Exporter, error) {
	timeoutCtx, cancel := context.WithCancelCause(ctx)
	timeoutCtx, _ = context.WithTimeoutCause(timeoutCtx, 5*time.Second, errors.WithStack(context.DeadlineExceeded)) //nolint:govet
	defer func() { cancel(errors.WithStack(context.Canceled)) }()

	caller, err := s.sm.Get(timeoutCtx, sessionID, false)
	if err != nil {
		return nil, err
	}

	client := sessionexporter.NewExporterClient(caller.Conn())
	ctx = caller.Context(ctx)
	var ids []string
	if err := inp.EachRef(func(ref cache.ImmutableRef) error {
		ids = append(ids, ref.ID())
		return nil
	}); err != nil {
		return nil, err
	}

	res, err := client.FindExporters(ctx, &sessionexporter.FindExportersRequest{
		Metadata: inp.Metadata,
		Refs:     ids,
	})
	if err != nil {
		switch grpcerrors.Code(err) {
		case codes.Unavailable, codes.Unimplemented:
			return nil, nil
		default:
			return nil, err
		}
	}

	w, err := defaultResolver(s.workerController)()
	if err != nil {
		return nil, err
	}

	var out []Exporter
	for i, req := range res.Exporters {
		exp, err := w.Exporter(req.Type, s.sm)
		if err != nil {
			return nil, err
		}
		expi, err := exp.Resolve(ctx, req.Attrs)
		if err != nil {
			return nil, err
		}
		id := fmt.Sprint(id + i)
		out = append(out, Exporter{
			ExporterInstance: expi,
			ExporterIO:       NewIO(id),
		})
	}
	return out, nil
}

func runCacheExporters(ctx context.Context, exporters []RemoteCacheExporter, j *solver.Job, cached *result.Result[solver.CachedResult], inp *result.Result[cache.ImmutableRef]) (exporterResponses []*controlapi.ExporterResponse, err error) {
	eg, ctx := errgroup.WithContext(ctx)
	g := session.NewGroup(j.SessionID)
	exporterResponses = make([]*controlapi.ExporterResponse, len(exporters))
	for exID, exp := range exporters {
		id := fmt.Sprint(j.SessionID, "-cache-", exID)
		eg.Go(func() (err error) {
			err = inBuilderContext(ctx, j, exp.Name(), id, func(ctx context.Context, _ solver.JobContext) error {
				prepareDone := progress.OneOff(ctx, "preparing build cache for export")
				if err := result.EachRef(cached, inp, func(res solver.CachedResult, ref cache.ImmutableRef) error {
					ctx := withDescHandlerCacheOpts(ctx, ref)

					// Configure compression
					compressionConfig := exp.Config().Compression

					// all keys have same export chain so exporting others is not needed
					_, err = res.CacheKeys()[0].Exporter.ExportTo(ctx, exp, solver.CacheExportOpt{
						ResolveRemotes: workerRefResolver(cacheconfig.RefConfig{Compression: compressionConfig}, false, g),
						Mode:           exp.CacheExportMode,
						Session:        g,
						CompressionOpt: &compressionConfig,
					})
					return err
				}); err != nil {
					return prepareDone(err)
				}
				prepareDone(nil)
				finalizeDone := progress.OneOff(ctx, "sending cache export")
				resp, err := exp.Finalize(ctx)
				exporterResponses[exID] = &controlapi.ExporterResponse{
					Metadata: &controlapi.ExporterMetadata{
						Type: exp.Type(),
					},
					Data: resp,
				}
				return finalizeDone(err)
			})
			if exp.IgnoreError {
				err = nil
			}
			return err
		})
	}
	if err := eg.Wait(); err != nil {
		return nil, err
	}

	return exporterResponses, nil
}

func runInlineCacheExporter(ctx context.Context, e exporter.ExporterInstance, inlineExporter inlineCacheExporter, j *solver.Job, cached *result.Result[solver.CachedResult]) (*result.Result[*exptypes.InlineCacheEntry], error) {
	if inlineExporter == nil {
		return nil, nil
	}

	done := progress.OneOff(ctx, "preparing layers for inline cache")
	res, err := result.ConvertResult(cached, func(res solver.CachedResult) (*exptypes.InlineCacheEntry, error) {
		dtic, err := inlineCache(ctx, inlineExporter, res, e.Config().Compression(), session.NewGroup(j.SessionID))
		if err != nil {
			return nil, err
		}
		if dtic == nil {
			return nil, nil
		}
		return &exptypes.InlineCacheEntry{Data: dtic}, nil
	})
	return res, done(err)
}

func exporterVertexID(sessionID string, exporterIndex int) string {
	return fmt.Sprint(sessionID, "-export-", exporterIndex)
}

func (s *Solver) runExporters(ctx context.Context, ref string, exporters []Exporter, inlineCacheExporter inlineCacheExporter, job *solver.Job, cached *result.Result[solver.CachedResult], inp *exporter.Source) (exporterResponses []*controlapi.ExporterResponse, finalizers []exporter.FinalizeFunc, descrefs []exporter.DescriptorReference, err error) {
	warnings, err := verifier.CheckInvalidPlatforms(ctx, inp)
	if err != nil {
		return nil, nil, nil, err
	}

	eg, ctx := errgroup.WithContext(ctx)
	exporterResponses = make([]*controlapi.ExporterResponse, len(exporters))
	finalizeFuncs := make([]exporter.FinalizeFunc, len(exporters))
	descs := make([]exporter.DescriptorReference, len(exporters))
	var inlineCacheMu sync.Mutex
	for exID, exp := range exporters {
		id := exporterVertexID(job.SessionID, exID)
		eg.Go(func() error {
			return inBuilderContext(ctx, job, exp.Name(), id, func(ctx context.Context, _ solver.JobContext) error {
				span, ctx := tracing.StartSpan(ctx, exp.Name())
				defer span.End()

				if exID == 0 && len(warnings) > 0 {
					pw, _, _ := progress.NewFromContext(ctx)
					for _, w := range warnings {
						pw.Write(identity.NewID(), w)
					}
					if err := pw.Close(); err != nil {
						return err
					}
				}
				inlineCache := exptypes.InlineCache(func(ctx context.Context) (*result.Result[*exptypes.InlineCacheEntry], error) {
					inlineCacheMu.Lock() // ensure only one inline cache exporter runs at a time
					defer inlineCacheMu.Unlock()
					return runInlineCacheExporter(ctx, exp, inlineCacheExporter, job, cached)
				})

				var md map[string]string
				md, finalizeFuncs[exID], descs[exID], err = exp.Export(ctx, inp, exporter.ExportBuildInfo{
					Ref:         ref,
					SessionID:   job.SessionID,
					InlineCache: inlineCache,
					IO:          exp.ExporterIO,
				})
				exporterResponses[exID] = &controlapi.ExporterResponse{
					Metadata: &controlapi.ExporterMetadata{
						Type: exp.Type(),
					},
					Data: md,
				}
				if err != nil {
					return err
				}
				return nil
			})
		})
	}
	if err := eg.Wait(); err != nil {
		return nil, nil, nil, err
	}

	if len(exporters) == 0 && len(warnings) > 0 {
		err := inBuilderContext(ctx, job, "Verifying build result", identity.NewID(), func(ctx context.Context, _ solver.JobContext) error {
			pw, _, _ := progress.NewFromContext(ctx)
			for _, w := range warnings {
				pw.Write(identity.NewID(), w)
			}
			return pw.Close()
		})
		if err != nil {
			return nil, nil, nil, err
		}
	}

	return exporterResponses, finalizeFuncs, descs, nil
}

func splitCacheExporters(exporters []RemoteCacheExporter) (rest []RemoteCacheExporter, inline inlineCacheExporter) {
	rest = make([]RemoteCacheExporter, 0, len(exporters))
	for _, exp := range exporters {
		if ic, ok := asInlineCache(exp.Exporter); ok {
			inline = ic
			continue
		}
		rest = append(rest, exp)
	}
	return rest, inline
}

type inlineCacheExporter interface {
	solver.CacheExporterTarget
	ExportForLayers(context.Context, []digest.Digest) ([]byte, error)
}

func asInlineCache(e remotecache.Exporter) (inlineCacheExporter, bool) {
	ie, ok := e.(inlineCacheExporter)
	return ie, ok
}

func inlineCache(ctx context.Context, ie inlineCacheExporter, res solver.CachedResult, compressionopt compression.Config, g session.Group) ([]byte, error) {
	workerRef, ok := res.Sys().(*worker.WorkerRef)
	if !ok {
		return nil, errors.Errorf("invalid reference: %T", res.Sys())
	}

	remotes, err := workerRef.GetRemotes(ctx, true, cacheconfig.RefConfig{Compression: compressionopt}, false, g)
	if err != nil || len(remotes) == 0 {
		return nil, nil
	}
	remote := remotes[0]

	digests := make([]digest.Digest, 0, len(remote.Descriptors))
	for _, desc := range remote.Descriptors {
		digests = append(digests, desc.Digest)
	}

	ctx = withDescHandlerCacheOpts(ctx, workerRef.ImmutableRef)
	refCfg := cacheconfig.RefConfig{Compression: compressionopt}
	if _, err := res.CacheKeys()[0].Exporter.ExportTo(ctx, ie, solver.CacheExportOpt{
		ResolveRemotes: workerRefResolver(refCfg, true, g), // load as many compression blobs as possible
		Mode:           solver.CacheExportModeMin,
		Session:        g,
		CompressionOpt: &compressionopt, // cache possible compression variants
	}); err != nil {
		return nil, err
	}
	return ie.ExportForLayers(ctx, digests)
}

func withDescHandlerCacheOpts(ctx context.Context, ref cache.ImmutableRef) context.Context {
	return solver.WithCacheOptGetter(ctx, func(includeAncestors bool, keys ...any) map[any]any {
		vals := make(map[any]any)
		for _, k := range keys {
			if key, ok := k.(cache.DescHandlerKey); ok {
				if handler := ref.DescHandler(digest.Digest(key)); handler != nil {
					vals[k] = handler
				}
			}
		}
		return vals
	})
}

// NewIO creates a new exporter API for the specified id
func NewIO(id string) exporter.ExporterIO {
	apis := exporterAPIs{exporterID: id}
	return exporter.ExporterIO{
		CopyToCaller:   apis.CopyToCaller,
		CopyFileWriter: apis.CopyFileWriter,
	}
}

func (r exporterAPIs) CopyToCaller(ctx context.Context, fs fsutil.FS, c session.Caller, progress func(int, bool)) error {
	return filesync.CopyToCaller(ctx, fs, r.exporterID, c, progress)
}

func (r exporterAPIs) CopyFileWriter(ctx context.Context, md map[string]string, c session.Caller) (io.WriteCloser, error) {
	return filesync.CopyFileWriter(ctx, md, r.exporterID, c)
}

type exporterAPIs struct {
	exporterID string
}
