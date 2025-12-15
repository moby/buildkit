package gateway

import (
	"context"
	"fmt"
	"os"

	api "github.com/containerd/containerd/api/services/content/v1"
	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/leases"
	"github.com/containerd/containerd/v2/plugins/services/content/contentserver"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/executor"
	"github.com/moby/buildkit/exporter"
	"github.com/moby/buildkit/exporter/containerimage"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	"github.com/moby/buildkit/frontend"
	"github.com/moby/buildkit/frontend/gateway"
	"github.com/moby/buildkit/frontend/gateway/container"
	"github.com/moby/buildkit/frontend/gateway/forwarder"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/filesync"
	"github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/util/progress/logs"
	"github.com/moby/buildkit/worker"
	"github.com/pkg/errors"
	"google.golang.org/grpc"
)

const (
	keySource = "source"
)

type Opt struct {
	CacheManager   cache.Manager
	SessionManager *session.Manager
	ImageWriter    *containerimage.ImageWriter
	LeaseManager   leases.Manager

	WorkerInfo client.WorkerInfo
}

type gatewayExporter struct {
	opt Opt
}

func New(opt Opt) (exporter.Exporter, error) {
	im := &gatewayExporter{opt: opt}
	return im, nil
}

func (e *gatewayExporter) Resolve(ctx context.Context, id int, opts exporter.ResolveOpts) (exporter.ExporterInstance, error) {
	i := &gatewayExporterInstance{
		gatewayExporter: e,
		id:              id,
		opts:            opts,
		workerInfo: workerInfo{
			cm:   e.opt.CacheManager,
			info: e.opt.WorkerInfo,
		},
	}

	for k, v := range opts.Attrs {
		switch k {
		case keySource:
			i.image = v

		default:
			if i.meta == nil {
				i.meta = make(map[string][]byte)
			}
			i.meta[k] = []byte(v)
		}
	}
	return i, nil
}

type gatewayExporterInstance struct {
	*gatewayExporter
	id   int
	opts exporter.ResolveOpts

	workerInfo worker.Infos

	image string

	meta map[string][]byte
}

func (e *gatewayExporterInstance) ID() int {
	return e.id
}

func (e *gatewayExporterInstance) Name() string {
	return fmt.Sprintf("exporting using %s", e.image)
}

func (e *gatewayExporterInstance) Type() string {
	return client.ExporterGateway
}

func (e *gatewayExporterInstance) Opts() exporter.ResolveOpts {
	return e.opts
}

func (e *gatewayExporterInstance) Config() *exporter.Config {
	return exporter.NewConfig()
}

func (e *gatewayExporterInstance) Export(ctx context.Context, llbBridge frontend.FrontendLLBBridge, exec executor.Executor, src *exporter.Source, inlineCache exptypes.InlineCache, sessionID string) (_ map[string]string, descref exporter.DescriptorReference, err error) {
	c, err := forwarder.LLBBridgeToGatewayClient(ctx, llbBridge, exec, e.opts.FrontendAttrs, nil, e.workerInfo, sessionID, e.opt.SessionManager)
	if err != nil {
		return nil, nil, err
	}
	st, img, mfstDigest, err := gateway.LoadImage(ctx, c, e.image)
	if err != nil {
		return nil, nil, err
	}
	_ = mfstDigest

	def, err := st.Marshal(ctx)
	if err != nil {
		return nil, nil, err
	}

	res, err := llbBridge.Solve(ctx, frontend.SolveRequest{
		Definition: def.ToPB(),
	}, sessionID)
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		ctx := context.WithoutCancel(ctx)
		res.EachRef(func(ref solver.ResultProxy) error {
			return ref.Release(ctx)
		})
	}()
	if res.Ref == nil {
		return nil, nil, errors.Errorf("gateway source didn't return default result")
	}
	frontendDef := res.Ref.Definition()
	r, err := res.Ref.Result(ctx)
	if err != nil {
		return nil, nil, err
	}
	workerRef, ok := r.Sys().(*worker.WorkerRef)
	if !ok {
		return nil, nil, errors.Errorf("invalid ref: %T", r.Sys())
	}
	rootFS, err := workerRef.Worker.CacheManager().New(ctx, workerRef.ImmutableRef, session.NewGroup(sessionID))
	if err != nil {
		return nil, nil, err
	}
	defer rootFS.Release(context.TODO())

	opts := make(map[string]string)
	for k, v := range e.meta {
		opts[k] = string(v)
	}
	meta, err := gateway.GetEnv(*img, opts, e.workerInfo, sessionID)
	if err != nil {
		return nil, nil, err
	}
	meta.Env = append(meta.Env, "BUILDKIT_EXPORTER_TARGET="+e.opts.Target.String())

	err = gateway.CheckCaps(*img, opts, nil, mfstDigest)
	if err != nil {
		return nil, nil, err
	}

	caller, err := e.opt.SessionManager.Get(ctx, sessionID, false)
	if err != nil {
		return nil, nil, err
	}

	lbf := gateway.NewBridgeForwarder(ctx, llbBridge, exec, e.workerInfo, nil, sessionID, e.opt.SessionManager)
	lbf.SetResult(src.FrontendResult, nil)

	attachables := []session.Attachable{}
	attachables = append(attachables, &proxyStore{lbf.Store(e.opt.ImageWriter.ContentStore())})
	switch e.opts.Target {
	case exptypes.ExporterTargetFile:
		attachables = append(attachables, &filesync.ProxyStreamWriter{Caller: caller, ExporterID: e.id})
	case exptypes.ExporterTargetDirectory:
		attachables = append(attachables, &filesync.ProxyDiffCopy{Caller: caller, ExporterID: e.id})
	}

	ctx = lbf.Serve(ctx, attachables...)
	defer lbf.Close()
	defer lbf.Discard()

	mdmnt, release, err := gateway.MetadataMount(frontendDef)
	if err != nil {
		return nil, nil, err
	}
	if release != nil {
		defer release()
	}
	var mnts []executor.Mount
	if mdmnt != nil {
		mnts = append(mnts, *mdmnt)
	}

	stdout, stderr, flush := logs.NewLogStreams(ctx, os.Getenv("BUILDKIT_DEBUG_EXEC_OUTPUT") == "1")
	defer stdout.Close()
	defer stderr.Close()
	defer func() {
		if err != nil {
			flush()
		}
	}()

	connIn, connOut := lbf.Conn()
	_, err = exec.Run(ctx, "", container.MountWithSession(rootFS, session.NewGroup(sessionID)), mnts, executor.ProcessInfo{Meta: *meta, Stdin: connIn, Stdout: connOut, Stderr: stderr}, nil)
	if err != nil {
		lbf.SetResult(nil, err)
	}

	_, err = lbf.Result(ctx)
	return nil, nil, err
}

type workerInfo struct {
	cm   cache.Manager
	info client.WorkerInfo
}

func (i workerInfo) DefaultCacheManager() (cache.Manager, error) {
	return i.cm, nil
}

func (i workerInfo) WorkerInfos() []client.WorkerInfo {
	return []client.WorkerInfo{i.info}
}

type proxyStore struct {
	store content.Store
}

func (s *proxyStore) Register(server *grpc.Server) {
	service := contentserver.New(s.store)
	api.RegisterContentServer(server, service)
}
