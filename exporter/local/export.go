package local

import (
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"time"

	"github.com/docker/docker/pkg/idtools"
	intoto "github.com/in-toto/in-toto-golang/in_toto"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/exporter"
	"github.com/moby/buildkit/exporter/attestation"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	"github.com/moby/buildkit/exporter/util/epoch"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/filesync"
	"github.com/moby/buildkit/snapshot"
	"github.com/moby/buildkit/solver/result"
	"github.com/moby/buildkit/util/progress"
	"github.com/moby/buildkit/util/staticfs"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
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
	tm, _, err := epoch.ParseAttr(opt)
	if err != nil {
		return nil, err
	}

	i := &localExporterInstance{localExporter: e, epoch: tm}

	for k, v := range opt {
		switch k {
		case keyAttestationPrefix:
			i.attestationPrefix = v
		}
	}

	return i, nil
}

type localExporterInstance struct {
	*localExporter
	epoch             *time.Time
	attestationPrefix string
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

	now := time.Now().Truncate(time.Second)

	export := func(ctx context.Context, k string, p *ocispecs.Platform, ref cache.ImmutableRef, attestations []result.Attestation) func() error {
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

			outputFS := fsutil.NewFS(src, walkOpt)

			attestations, err = attestation.Unbundle(ctx, session.NewGroup(sessionID), inp.Refs, attestations)
			if err != nil {
				return err
			}
			if len(attestations) > 0 {
				subjects := []intoto.Subject{}
				err = outputFS.Walk(ctx, func(path string, info fs.FileInfo, err error) error {
					if err != nil {
						return err
					}
					if !info.Mode().IsRegular() {
						return nil
					}
					f, err := outputFS.Open(path)
					if err != nil {
						return err
					}
					defer f.Close()
					d := digest.Canonical.Digester()
					if _, err := io.Copy(d.Hash(), f); err != nil {
						return err
					}
					subjects = append(subjects, intoto.Subject{
						Name:   path,
						Digest: result.ToDigestMap(d.Digest()),
					})
					return nil
				})
				if err != nil {
					return err
				}

				stmts, err := attestation.Generate(ctx, session.NewGroup(sessionID), inp.Refs, attestations, subjects)
				if err != nil {
					return err
				}
				stmtFS := staticfs.NewFS()

				names := map[string]struct{}{}
				for i, stmt := range stmts {
					dt, err := json.Marshal(stmt)
					if err != nil {
						return errors.Wrap(err, "failed to marshal attestation")
					}

					if attestations[i].Path == "" {
						return errors.New("attestation does not have set path")
					}
					name := e.attestationPrefix + path.Base(attestations[i].Path)
					if _, ok := names[name]; ok {
						return errors.Errorf("duplicate attestation path name %s", name)
					}
					names[name] = struct{}{}

					st := fstypes.Stat{
						Mode:    0600,
						Path:    name,
						ModTime: now.UnixNano(),
					}
					stmtFS.Add(name, st, dt)
				}

				outputFS = staticfs.NewMergeFS(outputFS, stmtFS)
			}

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

	if isMap {
		for _, p := range p.Platforms {
			r, ok := inp.Refs[p.ID]
			if !ok {
				return nil, errors.Errorf("failed to find ref for ID %s", p.ID)
			}
			eg.Go(export(ctx, p.ID, &p.Platform, r, inp.Attestations[p.ID]))
		}
	} else {
		eg.Go(export(ctx, "", nil, inp.Ref, nil))
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
