package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"github.com/docker/distribution/reference"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/executor"
	"github.com/moby/buildkit/frontend"
	pb "github.com/moby/buildkit/frontend/gateway/pb"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/session"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"golang.org/x/net/context"
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

func NewGatewayFrontend() frontend.Frontend {
	return &gatewayFrontend{}
}

type gatewayFrontend struct {
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

func (gf *gatewayFrontend) Solve(ctx context.Context, llbBridge frontend.FrontendLLBBridge, opts map[string]string) (retRef cache.ImmutableRef, exporterAttr map[string][]byte, retErr error) {

	source, ok := opts[keySource]
	if !ok {
		return nil, nil, errors.Errorf("no source specified for gateway")
	}

	sid := session.FromContext(ctx)

	_, isDevel := opts[keyDevel]
	var img ocispec.Image
	var rootFS cache.ImmutableRef

	if isDevel {
		ref, exp, err := llbBridge.Solve(session.NewContext(ctx, "gateway:"+sid),
			frontend.SolveRequest{
				Frontend:    source,
				FrontendOpt: filterPrefix(opts, "gateway-"),
			})
		if err != nil {
			return nil, nil, err
		}
		rootFS = ref
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

		dgst, config, err := llbBridge.ResolveImageConfig(ctx, sourceRef.String())
		if err != nil {
			return nil, nil, err
		}

		if err := json.Unmarshal(config, &img); err != nil {
			return nil, nil, err
		}

		sourceRef, err = reference.WithDigest(sourceRef, dgst)
		if err != nil {
			return nil, nil, err
		}

		src := llb.Image(sourceRef.String())

		def, err := src.Marshal()
		if err != nil {
			return nil, nil, err
		}

		ref, _, err := llbBridge.Solve(ctx, frontend.SolveRequest{
			Definition: def.ToPB(),
		})
		if err != nil {
			return nil, nil, err
		}
		rootFS = ref
	}

	lbf, err := newLLBBrideForwarder(ctx, llbBridge)
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

	err = llbBridge.Exec(ctx, executor.Meta{
		Env:  env,
		Args: args,
		Cwd:  cwd,
	}, rootFS, lbf.Stdin, lbf.Stdout, os.Stderr)

	if err != nil {
		return nil, nil, err
	}

	return lbf.lastRef, lbf.exporterAttr, nil
}

func newLLBBrideForwarder(ctx context.Context, llbBridge frontend.FrontendLLBBridge) (*llbBrideForwarder, error) {
	lbf := &llbBrideForwarder{
		llbBridge: llbBridge,
		refs:      map[string]cache.ImmutableRef{},
		pipe:      newPipe(),
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

type llbBrideForwarder struct {
	llbBridge    frontend.FrontendLLBBridge
	refs         map[string]cache.ImmutableRef
	lastRef      cache.ImmutableRef
	exporterAttr map[string][]byte
	*pipe
}

func (lbf *llbBrideForwarder) ResolveImageConfig(ctx context.Context, req *pb.ResolveImageConfigRequest) (*pb.ResolveImageConfigResponse, error) {
	dgst, dt, err := lbf.llbBridge.ResolveImageConfig(ctx, req.Ref)
	if err != nil {
		return nil, err
	}
	return &pb.ResolveImageConfigResponse{
		Digest: dgst,
		Config: dt,
	}, nil
}

func (lbf *llbBrideForwarder) Solve(ctx context.Context, req *pb.SolveRequest) (*pb.SolveResponse, error) {
	ref, expResp, err := lbf.llbBridge.Solve(ctx, frontend.SolveRequest{
		Definition: req.Definition,
		Frontend:   req.Frontend,
	})
	if err != nil {
		return nil, err
	}

	exp := map[string][]byte{}
	if err := json.Unmarshal(req.ExporterAttr, &exp); err != nil {
		return nil, err
	}

	if expResp != nil {
		for k, v := range expResp {
			exp[k] = v
		}
	}

	id := identity.NewID()
	lbf.refs[id] = ref
	if req.Final {
		lbf.lastRef = ref
		lbf.exporterAttr = exp
	}
	return &pb.SolveResponse{Ref: id}, nil
}
func (lbf *llbBrideForwarder) ReadFile(ctx context.Context, req *pb.ReadFileRequest) (*pb.ReadFileResponse, error) {
	ref, ok := lbf.refs[req.Ref]
	if !ok {
		return nil, errors.Errorf("no such ref: %v", req.Ref)
	}

	dt, err := cache.ReadFile(ctx, ref, req.FilePath)
	if err != nil {
		return nil, err
	}

	return &pb.ReadFileResponse{Data: dt}, nil
}

func (lbf *llbBrideForwarder) Ping(context.Context, *pb.PingRequest) (*pb.PongResponse, error) {
	return &pb.PongResponse{}, nil
}

func serve(ctx context.Context, grpcServer *grpc.Server, conn net.Conn) {
	go func() {
		<-ctx.Done()
		conn.Close()
	}()
	logrus.Debugf("serving grpc connection")
	(&http2.Server{}).ServeConn(conn, &http2.ServeConnOpts{Handler: grpcServer})
}
