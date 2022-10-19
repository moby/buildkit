package local

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/docker/docker/pkg/idtools"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/exporter"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	"github.com/moby/buildkit/exporter/util/epoch"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/filesync"
	"github.com/moby/buildkit/snapshot"
	"github.com/moby/buildkit/util/progress"
	"github.com/pkg/errors"
	"github.com/tonistiigi/fsutil"
	fstypes "github.com/tonistiigi/fsutil/types"
	"golang.org/x/sync/errgroup"
	"golang.org/x/time/rate"
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
	tm, _, err := epoch.ParseAttr(opt)
	if err != nil {
		return nil, err
	}

	return &localExporterInstance{localExporter: e, epoch: tm}, nil
}

type localExporterInstance struct {
	*localExporter
	epoch *time.Time
}

func (e *localExporterInstance) Name() string {
	return "exporting to client"
}

func (e *localExporter) Config() exporter.Config {
	return exporter.Config{}
}

func (e *localExporterInstance) Export(ctx context.Context, inp *exporter.Source, sessionID string) (map[string]string, error) {
	timeoutCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if e.epoch == nil {
		if tm, ok, err := epoch.ParseSource(inp); err != nil {
			return nil, err
		} else if ok {
			e.epoch = tm
		}
	}

	caller, err := e.opt.SessionManager.Get(timeoutCtx, sessionID, false)
	if err != nil {
		return nil, err
	}

	isMap := len(inp.Refs) > 0

	platformsBytes, ok := inp.Metadata[exptypes.ExporterPlatformsKey]
	if isMap && !ok {
		return nil, errors.Errorf("unable to export multiple refs, missing platforms mapping")
	}

	var p exptypes.Platforms
	if ok && len(platformsBytes) > 0 {
		if err := json.Unmarshal(platformsBytes, &p); err != nil {
			return nil, errors.Wrapf(err, "failed to parse platforms passed to exporter")
		}
	}

	export := func(ctx context.Context, k string, ref cache.ImmutableRef) func() error {
		return func() error {
			var src string
			var err error
			var idmap *idtools.IdentityMapping
			if ref == nil {
				src, err = os.MkdirTemp("", "buildkit")
				if err != nil {
					return err
				}
				defer os.RemoveAll(src)
			} else {
				mount, err := ref.Mount(ctx, true, session.NewGroup(sessionID))
				if err != nil {
					return err
				}

				lm := snapshot.LocalMounter(mount)

				src, err = lm.Mount()
				if err != nil {
					return err
				}

				idmap = mount.IdentityMapping()

				defer lm.Unmount()
			}

			walkOpt := &fsutil.WalkOpt{}
			var idMapFunc func(p string, st *fstypes.Stat) fsutil.MapResult

			if idmap != nil {
				idMapFunc = func(p string, st *fstypes.Stat) fsutil.MapResult {
					uid, gid, err := idmap.ToContainer(idtools.Identity{
						UID: int(st.Uid),
						GID: int(st.Gid),
					})
					if err != nil {
						return fsutil.MapResultExclude
					}
					st.Uid = uint32(uid)
					st.Gid = uint32(gid)
					return fsutil.MapResultKeep
				}
			}

			walkOpt.Map = func(p string, st *fstypes.Stat) fsutil.MapResult {
				res := fsutil.MapResultKeep
				if idMapFunc != nil {
					res = idMapFunc(p, st)
				}
				if e.epoch != nil {
					st.ModTime = e.epoch.UnixNano()
				}
				return res
			}

			fs := fsutil.NewFS(src, walkOpt)
			lbl := "copying files"
			if isMap {
				lbl += " " + k
				st := fstypes.Stat{
					Mode: uint32(os.ModeDir | 0755),
					Path: strings.Replace(k, "/", "_", -1),
				}
				if e.epoch != nil {
					st.ModTime = e.epoch.UnixNano()
				}
				fs, err = fsutil.SubDirFS([]fsutil.Dir{{FS: fs, Stat: st}})
				if err != nil {
					return err
				}
			}

			progress := newProgressHandler(ctx, lbl)
			if err := filesync.CopyToCaller(ctx, fs, caller, progress); err != nil {
				return err
			}
			return nil
		}
	}

	eg, ctx := errgroup.WithContext(ctx)

	if isMap {
		for _, p := range p.Platforms {
			r, ok := inp.Refs[p.ID]
			if !ok {
				return nil, errors.Errorf("failed to find ref for ID %s", p.ID)
			}
			eg.Go(export(ctx, p.ID, r))
		}
	} else {
		eg.Go(export(ctx, "", inp.Ref))
	}

	if err := eg.Wait(); err != nil {
		return nil, err
	}
	return nil, nil
}

func newProgressHandler(ctx context.Context, id string) func(int, bool) {
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
