package local

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
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
)

const (
	// preferNondistLayersKey is an exporter option which can be used to mark a layer as non-distributable if the layer reference was
	// already found to use a non-distributable media type.
	// When this option is not set, the exporter will change the media type of the layer to a distributable one.
	preferNondistLayersKey = "prefer-nondist-layers"
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
	li := &localExporterInstance{localExporter: e}

	tm, _, err := epoch.ParseAttr(opt)
	if err != nil {
		return nil, err
	}
	li.epoch = tm

	v, ok := opt[preferNondistLayersKey]
	if ok {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return nil, errors.Wrapf(err, "non-bool value for %s: %s", preferNondistLayersKey, v)
		}
		li.preferNonDist = b
	}

	return li, nil
}

type localExporterInstance struct {
	*localExporter
	preferNonDist bool
	epoch         *time.Time
}

func (e *localExporterInstance) Name() string {
	return "exporting to client"
}

func (e *localExporterInstance) Config() exporter.Config {
	return exporter.Config{}
}

func (e *localExporterInstance) Export(ctx context.Context, inp *exporter.Source, sessionID string) (map[string]string, error) {
	var defers []func()

	defer func() {
		for i := len(defers) - 1; i >= 0; i-- {
			defers[i]()
		}
	}()

	if e.epoch == nil {
		if tm, ok, err := epoch.ParseSource(inp); err != nil {
			return nil, err
		} else if ok {
			e.epoch = tm
		}
	}

	getDir := func(ctx context.Context, k string, ref cache.ImmutableRef) (*fsutil.Dir, error) {
		var src string
		var err error
		var idmap *idtools.IdentityMapping
		if ref == nil {
			src, err = os.MkdirTemp("", "buildkit")
			if err != nil {
				return nil, err
			}
			defers = append(defers, func() { os.RemoveAll(src) })
		} else {
			mount, err := ref.Mount(ctx, true, session.NewGroup(sessionID))
			if err != nil {
				return nil, err
			}

			lm := snapshot.LocalMounter(mount)

			src, err = lm.Mount()
			if err != nil {
				return nil, err
			}

			idmap = mount.IdentityMapping()

			defers = append(defers, func() { lm.Unmount() })
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

		st := fstypes.Stat{
			Mode: uint32(os.ModeDir | 0755),
			Path: strings.Replace(k, "/", "_", -1),
		}
		if e.epoch != nil {
			st.ModTime = e.epoch.UnixNano()
		}

		return &fsutil.Dir{
			FS:   fsutil.NewFS(src, walkOpt),
			Stat: st,
		}, nil
	}

	platformsBytes, ok := inp.Metadata[exptypes.ExporterPlatformsKey]
	if len(inp.Refs) > 0 && !ok {
		return nil, errors.Errorf("unable to export multiple refs, missing platforms mapping")
	}

	var p exptypes.Platforms
	if ok && len(platformsBytes) > 0 {
		if err := json.Unmarshal(platformsBytes, &p); err != nil {
			return nil, errors.Wrapf(err, "failed to parse platforms passed to exporter")
		}
	}

	var fs fsutil.FS

	if len(inp.Refs) > 0 {
		dirs := make([]fsutil.Dir, 0, len(p.Platforms))
		for _, p := range p.Platforms {
			r, ok := inp.Refs[p.ID]
			if !ok {
				return nil, errors.Errorf("failed to find ref for ID %s", p.ID)
			}
			d, err := getDir(ctx, p.ID, r)
			if err != nil {
				return nil, err
			}
			dirs = append(dirs, *d)
		}
		var err error
		fs, err = fsutil.SubDirFS(dirs)
		if err != nil {
			return nil, err
		}
	} else {
		d, err := getDir(ctx, "", inp.Ref)
		if err != nil {
			return nil, err
		}
		fs = d.FS
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	caller, err := e.opt.SessionManager.Get(timeoutCtx, sessionID, false)
	if err != nil {
		return nil, err
	}

	w, err := filesync.CopyFileWriter(ctx, nil, caller)
	if err != nil {
		return nil, err
	}
	report := progress.OneOff(ctx, "sending tarball")
	if err := fsutil.WriteTar(ctx, fs, w); err != nil {
		w.Close()
		return nil, report(err)
	}
	return nil, report(w.Close())
}
