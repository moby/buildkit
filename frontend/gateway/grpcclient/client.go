package grpcclient

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"github.com/moby/buildkit/frontend/gateway/client"
	pb "github.com/moby/buildkit/frontend/gateway/pb"
	opspb "github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/apicaps"
	digest "github.com/opencontainers/go-digest"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"google.golang.org/grpc"
)

const frontendPrefix = "BUILDKIT_FRONTEND_OPT_"

func Current() (client.Client, error) {
	ctx, conn, err := grpcClientConn(context.Background())
	if err != nil {
		return nil, err
	}

	c := pb.NewLLBBridgeClient(conn)

	resp, err := c.Ping(ctx, &pb.PingRequest{})
	if err != nil {
		return nil, err
	}

	if resp.FrontendAPICaps == nil {
		resp.FrontendAPICaps = defaultCaps()
	}

	return &grpcClient{
		client:    c,
		opts:      opts(),
		sessionID: sessionID(),
		workers:   workers(),
		caps:      pb.Caps.CapSet(resp.FrontendAPICaps),
	}, nil
}

// defaultCaps returns the capabilities that were implemented when capabilities
// support was added. This list is frozen and should never be changed.
func defaultCaps() []apicaps.PBCap {
	return []apicaps.PBCap{
		{ID: string(pb.CapSolveBase), Enabled: true},
		{ID: string(pb.CapSolveInlineReturn), Enabled: true},
		{ID: string(pb.CapResolveImage), Enabled: true},
		{ID: string(pb.CapReadFile), Enabled: true},
	}
}

type grpcClient struct {
	client    pb.LLBBridgeClient
	opts      map[string]string
	sessionID string
	workers   []client.WorkerInfo
	caps      apicaps.CapSet
}

func (c *grpcClient) Solve(ctx context.Context, creq client.SolveRequest, exporterAttr map[string][]byte, final bool) (client.Reference, error) {
	dt, err := json.Marshal(exporterAttr)
	if err != nil {
		return nil, err
	}
	req := &pb.SolveRequest{
		Definition:      creq.Definition,
		Frontend:        creq.Frontend,
		FrontendOpt:     creq.FrontendOpt,
		ImportCacheRefs: creq.ImportCacheRefs,
		Final:           final,
		ExporterAttr:    dt,
	}
	resp, err := c.client.Solve(ctx, req)
	if err != nil {
		return nil, err
	}
	if resp.Ref == "" {
		return nil, nil
	}
	return &reference{id: resp.Ref, c: c}, nil
}

func (c *grpcClient) ResolveImageConfig(ctx context.Context, ref string, platform *specs.Platform) (digest.Digest, []byte, error) {
	var p *opspb.Platform
	if platform != nil {
		p = &opspb.Platform{
			OS:           platform.OS,
			Architecture: platform.Architecture,
			Variant:      platform.Variant,
			OSVersion:    platform.OSVersion,
			OSFeatures:   platform.OSFeatures,
		}
	}
	resp, err := c.client.ResolveImageConfig(ctx, &pb.ResolveImageConfigRequest{Ref: ref, Platform: p})
	if err != nil {
		return "", nil, err
	}
	return resp.Digest, resp.Config, nil
}

func (c *grpcClient) Opts() map[string]string {
	return c.opts
}

func (c *grpcClient) SessionID() string {
	return c.sessionID
}

func (c *grpcClient) WorkerInfos() []client.WorkerInfo {
	return c.workers
}

type reference struct {
	id string
	c  *grpcClient
}

func (r *reference) ReadFile(ctx context.Context, req client.ReadRequest) ([]byte, error) {
	rfr := &pb.ReadFileRequest{FilePath: req.Filename, Ref: r.id}
	if r := req.Range; r != nil {
		rfr.Range = &pb.FileRange{
			Offset: int64(r.Offset),
			Length: int64(r.Length),
		}
	}
	resp, err := r.c.client.ReadFile(ctx, rfr)
	if err != nil {
		return nil, err
	}
	return resp.Data, nil
}

func grpcClientConn(ctx context.Context) (context.Context, *grpc.ClientConn, error) {
	dialOpt := grpc.WithDialer(func(addr string, d time.Duration) (net.Conn, error) {
		return stdioConn(), nil
	})

	cc, err := grpc.DialContext(ctx, "", dialOpt, grpc.WithInsecure())
	if err != nil {
		return nil, nil, errors.Wrap(err, "failed to create grpc client")
	}

	ctx, cancel := context.WithCancel(ctx)
	_ = cancel
	// go monitorHealth(ctx, cc, cancel)

	return ctx, cc, nil
}

func stdioConn() net.Conn {
	return &conn{os.Stdin, os.Stdout, os.Stdout}
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

func opts() map[string]string {
	opts := map[string]string{}
	for _, env := range os.Environ() {
		parts := strings.SplitN(env, "=", 2)
		k := parts[0]
		v := ""
		if len(parts) == 2 {
			v = parts[1]
		}
		if !strings.HasPrefix(k, frontendPrefix) {
			continue
		}
		parts = strings.SplitN(v, "=", 2)
		v = ""
		if len(parts) == 2 {
			v = parts[1]
		}
		opts[parts[0]] = v
	}
	return opts
}

func sessionID() string {
	return os.Getenv("BUILDKIT_SESSION_ID")
}

func workers() []client.WorkerInfo {
	var c []client.WorkerInfo
	if err := json.Unmarshal([]byte(os.Getenv("BUILDKIT_WORKERS")), &c); err != nil {
		return nil
	}
	return c
}
