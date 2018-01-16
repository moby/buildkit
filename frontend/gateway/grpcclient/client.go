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
	digest "github.com/opencontainers/go-digest"
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

	_, err = c.Ping(ctx, &pb.PingRequest{})
	if err != nil {
		return nil, err
	}

	return &grpcClient{client: c, opts: opts(), sessionID: sessionID()}, nil
}

type grpcClient struct {
	client    pb.LLBBridgeClient
	opts      map[string]string
	sessionID string
}

func (c *grpcClient) Solve(ctx context.Context, def *opspb.Definition, frontend string, exporterAttr map[string][]byte, final bool) (client.Reference, error) {
	dt, err := json.Marshal(exporterAttr)
	if err != nil {
		return nil, err
	}
	req := &pb.SolveRequest{Definition: def, Frontend: frontend, Final: final, ExporterAttr: dt}
	resp, err := c.client.Solve(ctx, req)
	if err != nil {
		return nil, err
	}
	if resp.Ref == "" {
		return nil, nil
	}
	return &reference{id: resp.Ref, c: c}, nil
}

func (c *grpcClient) ResolveImageConfig(ctx context.Context, ref string) (digest.Digest, []byte, error) {
	resp, err := c.client.ResolveImageConfig(ctx, &pb.ResolveImageConfigRequest{Ref: ref})
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

type reference struct {
	id string
	c  *grpcClient
}

func (r *reference) ReadFile(ctx context.Context, fp string) ([]byte, error) {
	resp, err := r.c.client.ReadFile(ctx, &pb.ReadFileRequest{FilePath: fp, Ref: r.id})
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
