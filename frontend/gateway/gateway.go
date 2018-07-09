package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/docker/distribution/reference"
	apitypes "github.com/moby/buildkit/api/types"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/executor"
	"github.com/moby/buildkit/frontend"
	pb "github.com/moby/buildkit/frontend/gateway/pb"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/solver"
	opspb "github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/apicaps"
	"github.com/moby/buildkit/util/tracing"
	"github.com/moby/buildkit/worker"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"golang.org/x/net/http2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
)

const (
	keySource           = "source"
	keyDevel            = "gateway-devel"
	exporterImageConfig = "containerimage.config"
)

func NewGatewayFrontend(w frontend.WorkerInfos) frontend.Frontend {
	return &gatewayFrontend{
		workers: w,
	}
}

type gatewayFrontend struct {
	workers frontend.WorkerInfos
}

func filterPrefix(opts map[string]string, pfx string) map[string]string {
	m := map[string]string{}
	for k, v := range opts {
		if strings.HasPrefix(k, pfx) {
			m[strings.TrimPrefix(k, pfx)] = v
		}
	}
	return m
}

func (gf *gatewayFrontend) Solve(ctx context.Context, llbBridge frontend.FrontendLLBBridge, opts map[string]string) (retRef map[string]solver.CachedResult, exporterAttr map[string][]byte, retErr error) {
	source, ok := opts[keySource]
	if !ok {
		return nil, nil, errors.Errorf("no source specified for gateway")
	}

	sid := session.FromContext(ctx)

	_, isDevel := opts[keyDevel]
	var img specs.Image
	var rootFS cache.ImmutableRef
	var readonly bool // TODO: try to switch to read-only by default.

	if isDevel {
		res, exp, err := llbBridge.Solve(session.NewContext(ctx, "gateway:"+sid),
			frontend.SolveRequest{
				Frontend:    source,
				FrontendOpt: filterPrefix(opts, "gateway-"),
			})
		if err != nil {
			return nil, nil, err
		}
		defer func() {
			for _, ref := range res {
				ref.Release(context.TODO())
			}
		}()
		if l := len(res); l != 1 {
			return nil, nil, errors.Errorf("development gateway returned invalid length of results: %d", l)
		}
		if _, ok := res["default"]; !ok {
			return nil, nil, errors.Errorf("development gateway didn't return default result")
		}
		ref := res["default"]

		workerRef, ok := ref.Sys().(*worker.WorkerRef)
		if !ok {
			return nil, nil, errors.Errorf("invalid ref: %T", ref.Sys())
		}
		rootFS = workerRef.ImmutableRef
		config, ok := exp[exporterImageConfig]
		if ok {
			if err := json.Unmarshal(config, &img); err != nil {
				return nil, nil, err
			}
		}
	} else {
		sourceRef, err := reference.ParseNormalizedNamed(source)
		if err != nil {
			return nil, nil, err
		}

		dgst, config, err := llbBridge.ResolveImageConfig(ctx, reference.TagNameOnly(sourceRef).String(), nil) // TODO:
		if err != nil {
			return nil, nil, err
		}

		if err := json.Unmarshal(config, &img); err != nil {
			return nil, nil, err
		}

		if dgst != "" {
			sourceRef, err = reference.WithDigest(sourceRef, dgst)
			if err != nil {
				return nil, nil, err
			}
		}

		src := llb.Image(sourceRef.String())

		def, err := src.Marshal()
		if err != nil {
			return nil, nil, err
		}

		res, _, err := llbBridge.Solve(ctx, frontend.SolveRequest{
			Definition: def.ToPB(),
		})
		if err != nil {
			return nil, nil, err
		}
		defer func() {
			for _, ref := range res {
				ref.Release(context.TODO())
			}
		}()
		if l := len(res); l != 1 {
			return nil, nil, errors.Errorf("frontend image returned invalid length of results: %d", l)
		}
		if _, ok := res["default"]; !ok {
			return nil, nil, errors.Errorf("frontend image gateway didn't return default result")
		}
		ref := res["default"]
		workerRef, ok := ref.Sys().(*worker.WorkerRef)
		if !ok {
			return nil, nil, errors.Errorf("invalid ref: %T", ref.Sys())
		}
		rootFS = workerRef.ImmutableRef
	}

	lbf, err := newLLBBridgeForwarder(ctx, llbBridge, gf.workers)
	defer lbf.conn.Close()
	if err != nil {
		return nil, nil, err
	}

	args := []string{"/run"}
	env := []string{}
	cwd := "/"
	if img.Config.Env != nil {
		env = img.Config.Env
	}
	if img.Config.Entrypoint != nil {
		args = img.Config.Entrypoint
	}
	if img.Config.WorkingDir != "" {
		cwd = img.Config.WorkingDir
	}
	i := 0
	for k, v := range opts {
		env = append(env, fmt.Sprintf("BUILDKIT_FRONTEND_OPT_%d", i)+"="+k+"="+v)
		i++
	}

	env = append(env, "BUILDKIT_SESSION_ID="+sid)

	dt, err := json.Marshal(gf.workers.WorkerInfos())
	if err != nil {
		return nil, nil, errors.Wrap(err, "failed to marshal workers array")
	}
	env = append(env, "BUILDKIT_WORKERS="+string(dt))

	defer func() {
		for _, r := range lbf.refs {
			if retErr != nil && len(lbf.lastRefs) > 0 {
				for _, ref := range lbf.lastRefs {
					if ref == r {
						continue
					}
				}
			}
			if r != nil && (lbf.lastRef != r || retErr != nil) {
				r.Release(context.TODO())
			}
		}
	}()

	env = append(env, "BUILDKIT_EXPORTEDPRODUCT="+apicaps.ExportedProduct)

	err = llbBridge.Exec(ctx, executor.Meta{
		Env:            env,
		Args:           args,
		Cwd:            cwd,
		ReadonlyRootFS: readonly,
	}, rootFS, lbf.Stdin, lbf.Stdout, os.Stderr)

	if lbf.err != nil {
		return nil, nil, lbf.err
	}

	if err != nil {
		return nil, nil, err
	}

	if lbf.lastRefs != nil {
		return lbf.lastRefs, lbf.exporterAttr, nil
	}

	return map[string]solver.CachedResult{"default": lbf.lastRef}, lbf.exporterAttr, nil
}

func newLLBBridgeForwarder(ctx context.Context, llbBridge frontend.FrontendLLBBridge, workers frontend.WorkerInfos) (*llbBridgeForwarder, error) {
	lbf := &llbBridgeForwarder{
		callCtx:   ctx,
		llbBridge: llbBridge,
		refs:      map[string]solver.CachedResult{},
		pipe:      newPipe(),
		workers:   workers,
	}

	server := grpc.NewServer()
	grpc_health_v1.RegisterHealthServer(server, health.NewServer())
	pb.RegisterLLBBridgeServer(server, lbf)

	go serve(ctx, server, lbf.conn)

	return lbf, nil
}

type pipe struct {
	Stdin  io.ReadCloser
	Stdout io.WriteCloser
	conn   net.Conn
}

func newPipe() *pipe {
	pr1, pw1, _ := os.Pipe()
	pr2, pw2, _ := os.Pipe()
	return &pipe{
		Stdin:  pr1,
		Stdout: pw2,
		conn: &conn{
			Reader: pr2,
			Writer: pw1,
			Closer: pw2,
		},
	}
}

type conn struct {
	io.Reader
	io.Writer
	io.Closer
}

func (s *conn) LocalAddr() net.Addr {
	return dummyAddr{}
}
func (s *conn) RemoteAddr() net.Addr {
	return dummyAddr{}
}
func (s *conn) SetDeadline(t time.Time) error {
	return nil
}
func (s *conn) SetReadDeadline(t time.Time) error {
	return nil
}
func (s *conn) SetWriteDeadline(t time.Time) error {
	return nil
}

type dummyAddr struct {
}

func (d dummyAddr) Network() string {
	return "pipe"
}

func (d dummyAddr) String() string {
	return "localhost"
}

type llbBridgeForwarder struct {
	mu           sync.Mutex
	callCtx      context.Context
	llbBridge    frontend.FrontendLLBBridge
	refs         map[string]solver.CachedResult
	lastRef      solver.CachedResult
	lastRefs     map[string]solver.CachedResult
	err          error
	exporterAttr map[string][]byte
	workers      frontend.WorkerInfos
	*pipe
}

func (lbf *llbBridgeForwarder) ResolveImageConfig(ctx context.Context, req *pb.ResolveImageConfigRequest) (*pb.ResolveImageConfigResponse, error) {
	ctx = tracing.ContextWithSpanFromContext(ctx, lbf.callCtx)
	var platform *specs.Platform
	if p := req.Platform; p != nil {
		platform = &specs.Platform{
			OS:           p.OS,
			Architecture: p.Architecture,
			Variant:      p.Variant,
			OSVersion:    p.OSVersion,
			OSFeatures:   p.OSFeatures,
		}
	}
	dgst, dt, err := lbf.llbBridge.ResolveImageConfig(ctx, req.Ref, platform)
	if err != nil {
		return nil, err
	}
	return &pb.ResolveImageConfigResponse{
		Digest: dgst,
		Config: dt,
	}, nil
}

func (lbf *llbBridgeForwarder) Solve(ctx context.Context, req *pb.SolveRequest) (*pb.SolveResponse, error) {
	ctx = tracing.ContextWithSpanFromContext(ctx, lbf.callCtx)
	res, expResp, err := lbf.llbBridge.Solve(ctx, frontend.SolveRequest{
		Definition:      req.Definition,
		Frontend:        req.Frontend,
		FrontendOpt:     req.FrontendOpt,
		ImportCacheRefs: req.ImportCacheRefs,
	})
	if err != nil {
		return nil, err
	}

	ids := make(map[string]string)

	lbf.mu.Lock()
	for k, ref := range res {
		id := identity.NewID()
		if ref == nil {
			id = ""
		}
		lbf.refs[id] = ref
		ids[k] = id
	}
	lbf.mu.Unlock()

	if _, ok := res["default"]; !ok && len(res) != 0 && !req.AllowMapReturn {
		// this should never happen because old client shouldn't make a map request
		return nil, errors.Errorf("solve did not return default result")
	}

	exp := map[string][]byte{}
	// compatibility mode for older clients
	if req.Final {
		if err := json.Unmarshal(req.ExporterAttr, &exp); err != nil {
			return nil, err
		}

		if expResp != nil {
			for k, v := range expResp {
				exp[k] = v
			}
		}

		lbf.lastRef = lbf.refs[ids["default"]]
		lbf.exporterAttr = exp
	}

	resp := &pb.SolveResponse{}

	if req.AllowMapReturn {
		resp.Refs = ids
		resp.Metadata = expResp
	} else {
		resp.Ref = ids["default"]
	}

	return resp, nil
}
func (lbf *llbBridgeForwarder) ReadFile(ctx context.Context, req *pb.ReadFileRequest) (*pb.ReadFileResponse, error) {
	ctx = tracing.ContextWithSpanFromContext(ctx, lbf.callCtx)
	lbf.mu.Lock()
	ref, ok := lbf.refs[req.Ref]
	lbf.mu.Unlock()
	if !ok {
		return nil, errors.Errorf("no such ref: %v", req.Ref)
	}
	if ref == nil {
		return nil, errors.Wrapf(os.ErrNotExist, "%s no found", req.FilePath)
	}
	workerRef, ok := ref.Sys().(*worker.WorkerRef)
	if !ok {
		return nil, errors.Errorf("invalid ref: %T", ref.Sys())
	}

	newReq := cache.ReadRequest{
		Filename: req.FilePath,
	}
	if r := req.Range; r != nil {
		newReq.Range = &cache.FileRange{
			Offset: int(r.Offset),
			Length: int(r.Length),
		}
	}

	dt, err := cache.ReadFile(ctx, workerRef.ImmutableRef, newReq)
	if err != nil {
		return nil, err
	}

	return &pb.ReadFileResponse{Data: dt}, nil
}

func (lbf *llbBridgeForwarder) Ping(context.Context, *pb.PingRequest) (*pb.PongResponse, error) {

	workers := lbf.workers.WorkerInfos()
	pbWorkers := make([]*apitypes.WorkerRecord, 0, len(workers))
	for _, w := range workers {
		pbWorkers = append(pbWorkers, &apitypes.WorkerRecord{
			ID:        w.ID,
			Labels:    w.Labels,
			Platforms: opspb.PlatformsFromSpec(w.Platforms),
		})
	}

	return &pb.PongResponse{
		FrontendAPICaps: pb.Caps.All(),
		Workers:         pbWorkers,
		// TODO: add LLB info
	}, nil
}

func (lbf *llbBridgeForwarder) Return(ctx context.Context, in *pb.ReturnRequest) (*pb.ReturnResponse, error) {
	if in.Error != nil {
		lbf.err = errors.Errorf(in.Error.Text)
	} else {
		lbf.exporterAttr = in.ExporterMeta
		refs, err := lbf.convertRefs(in.ExporterRefs)
		if err != nil {
			lbf.err = err
		} else {
			lbf.lastRefs = refs
		}
	}

	return &pb.ReturnResponse{}, nil
}

func (lbf *llbBridgeForwarder) convertRefs(refs map[string]string) (map[string]solver.CachedResult, error) {
	out := make(map[string]solver.CachedResult, len(refs))
	for k, v := range refs {
		if v == "" {
			out[k] = nil
		}
		r, ok := lbf.refs[v]
		if !ok {
			return nil, errors.Errorf("return reference %s not found", v)
		}
		out[k] = r
	}
	return out, nil
}

func serve(ctx context.Context, grpcServer *grpc.Server, conn net.Conn) {
	go func() {
		<-ctx.Done()
		conn.Close()
	}()
	logrus.Debugf("serving grpc connection")
	(&http2.Server{}).ServeConn(conn, &http2.ServeConnOpts{Handler: grpcServer})
}
