package local

import (
	"time"

	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/exporter"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/filesync"
	"github.com/moby/buildkit/snapshot"
	"github.com/moby/buildkit/util/progress"
	"github.com/pkg/errors"
	"golang.org/x/net/context"
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
	id := session.FromContext(ctx)
	if id == "" {
		return nil, errors.New("could not access local files without session")
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	caller, err := e.opt.SessionManager.Get(timeoutCtx, id)
	if err != nil {
		return nil, err
	}

	li := &localExporterInstance{localExporter: e, caller: caller}
	return li, nil
}

type localExporterInstance struct {
	*localExporter
	caller session.Caller
}

func (e *localExporterInstance) Name() string {
	return "exporting to client"
}

func (e *localExporterInstance) Export(ctx context.Context, ref cache.ImmutableRef) error {
	mount, err := ref.Mount(ctx, true)
	if err != nil {
		return err
	}

	lm := snapshot.LocalMounter(mount)

	dest, err := lm.Mount()
	if err != nil {
		return err
	}
	defer lm.Unmount()

	progress := newProgressHandler(ctx, "copying files")
	return filesync.CopyToCaller(ctx, dest, e.caller, progress)
}

func newProgressHandler(ctx context.Context, id string) func(int, bool) {
	limiter := rate.NewLimiter(rate.Every(100*time.Millisecond), 1)
	pw, _, _ := progress.FromContext(ctx)
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
