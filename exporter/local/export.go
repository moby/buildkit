package local

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/exporter"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	"github.com/moby/buildkit/exporter/util/epoch"
	"github.com/moby/buildkit/exporter/util/multiplatform"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/filesync"
	"github.com/moby/buildkit/solver/result"
	"github.com/moby/buildkit/util/progress"
	"github.com/pkg/errors"
	"github.com/tonistiigi/fsutil"
	fstypes "github.com/tonistiigi/fsutil/types"
	"golang.org/x/sync/errgroup"
	"golang.org/x/time/rate"
)

const (
	keyAttestationPrefix = "attestation-prefix"
)

type Opt struct {
	SessionManager *session.Manager
}

type localExporter struct {
	opt Opt
	// session manager
}

func New(opt Opt) (exporter.Exporter, error) {
	le := &localExporter{opt: opt}
	return le, nil
}

func (e *localExporter) Resolve(ctx context.Context, opt map[string]string) (exporter.ExporterInstance, error) {
	tm, _, err := epoch.ParseExporterAttrs(opt)
	if err != nil {
		return nil, err
	}

	multiPlatform, _, err := multiplatform.ParseExporterAttrs(opt)
	if err != nil {
		return nil, err
	}

	i := &localExporterInstance{
		localExporter: e,
		opts: CreateFSOpts{
			Epoch:         tm,
			MultiPlatform: multiPlatform,
		},
	}

	for k, v := range opt {
		switch k {
		case keyAttestationPrefix:
			i.opts.AttestationPrefix = v
		}
	}

	return i, nil
}

type localExporterInstance struct {
	*localExporter
	opts CreateFSOpts
}

func (e *localExporterInstance) Name() string {
	return "exporting to client directory"
}

func (e *localExporter) Config() *exporter.Config {
	return exporter.NewConfig()
}

func (e *localExporterInstance) Export(ctx context.Context, inp *exporter.Source, sessionID string) (map[string]string, error) {
	timeoutCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if e.opts.Epoch == nil {
		if tm, ok, err := epoch.ParseSource(inp); err != nil {
			return nil, err
		} else if ok {
			e.opts.Epoch = tm
		}
	}

	caller, err := e.opt.SessionManager.Get(timeoutCtx, sessionID, false)
	if err != nil {
		return nil, err
	}

	isMap := len(inp.Refs) > 0

	platformsBytes, ok := inp.Metadata[exptypes.ExporterPlatformsKey]
	if len(inp.Refs) > 0 && !ok {
		return nil, errors.Errorf("unable to export multiple refs, missing platforms mapping")
	}

	var p exptypes.Platforms
	if ok && len(platformsBytes) > 0 {
		if err := json.Unmarshal(platformsBytes, &p); err != nil {
			return nil, errors.Wrapf(err, "failed to parse platforms passed to exporter")
		}
		if len(p.Platforms) > 1 {
			isMap = true
		}
	}

	if e.opts.MultiPlatform != nil {
		isMap = *e.opts.MultiPlatform
	}
	if !isMap && len(p.Platforms) > 1 {
		return nil, errors.Errorf("unable to export multiple platforms without map")
	}

	now := time.Now().Truncate(time.Second)

	export := func(ctx context.Context, k string, ref cache.ImmutableRef, attestations []result.Attestation) func() error {
		return func() error {
			outputFS, cleanup, err := CreateFS(ctx, sessionID, k, ref, inp.Refs, attestations, now, e.opts)
			if err != nil {
				return err
			}
			if cleanup != nil {
				defer cleanup()
			}

			lbl := "copying files"
			if isMap {
				lbl += " " + k
				st := fstypes.Stat{
					Mode: uint32(os.ModeDir | 0755),
					Path: strings.Replace(k, "/", "_", -1),
				}
				if e.opts.Epoch != nil {
					st.ModTime = e.opts.Epoch.UnixNano()
				}

				outputFS, err = fsutil.SubDirFS([]fsutil.Dir{{FS: outputFS, Stat: st}})
				if err != nil {
					return err
				}
			}

			progress := NewProgressHandler(ctx, lbl)
			if err := filesync.CopyToCaller(ctx, outputFS, caller, progress); err != nil {
				return err
			}
			return nil
		}
	}

	eg, ctx := errgroup.WithContext(ctx)

	if len(inp.Refs) > 0 {
		for _, p := range p.Platforms {
			r, ok := inp.Refs[p.ID]
			if !ok {
				return nil, errors.Errorf("failed to find ref for ID %s", p.ID)
			}
			eg.Go(export(ctx, p.ID, r, inp.Attestations[p.ID]))
			if !isMap {
				break
			}
		}
	} else {
		eg.Go(export(ctx, "", inp.Ref, nil))
	}

	if err := eg.Wait(); err != nil {
		return nil, err
	}
	return nil, nil
}

func NewProgressHandler(ctx context.Context, id string) func(int, bool) {
	limiter := rate.NewLimiter(rate.Every(100*time.Millisecond), 1)
	pw, _, _ := progress.NewFromContext(ctx)
	now := time.Now()
	st := progress.Status{
		Started: &now,
		Action:  "transferring",
	}
	pw.Write(id, st)
	return func(s int, last bool) {
		if last || limiter.Allow() {
			st.Current = s
			if last {
				now := time.Now()
				st.Completed = &now
			}
			pw.Write(id, st)
			if last {
				pw.Close()
			}
		}
	}
}
