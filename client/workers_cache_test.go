package client

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	controlapi "github.com/moby/buildkit/api/services/control"
	apitypes "github.com/moby/buildkit/api/types"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/solver/pb"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
)

func TestListWorkersCachedForConnection(t *testing.T) {
	srv := &listWorkersServer{id: "worker-a"}
	lis := newTrackingListener(t)
	startControlServer(t, lis, srv)

	ctx := t.Context()
	c := newTestClient(t, lis)
	workers, err := c.ListWorkers(ctx)
	require.NoError(t, err)
	require.Equal(t, "worker-a", workers[0].ID)
	require.Equal(t, "overlayfs", workers[0].Labels["org.mobyproject.buildkit.worker.snapshotter"])

	workers[0].ID = "mutated"
	workers[0].Labels["org.mobyproject.buildkit.worker.snapshotter"] = "mutated"
	workers[0].Platforms[0].Architecture = "mutated"

	again, err := c.ListWorkers(ctx)
	require.NoError(t, err)
	require.Equal(t, "worker-a", again[0].ID)
	require.Equal(t, "overlayfs", again[0].Labels["org.mobyproject.buildkit.worker.snapshotter"])
	require.Equal(t, "amd64", again[0].Platforms[0].Architecture)
	require.Equal(t, 1, srv.calls())

	_, err = c.ListWorkers(ctx, WithFilter([]string{"id==worker-a"}))
	require.NoError(t, err)
	_, err = c.ListWorkers(ctx, WithFilter([]string{"id==worker-a"}))
	require.NoError(t, err)
	require.Equal(t, 3, srv.calls())
	_, err = c.ListWorkers(ctx)
	require.NoError(t, err)
	require.Equal(t, 3, srv.calls())

	// A new client is a new connection and must observe the server again.
	// Callers that open a short-lived client, such as the docker driver's
	// snapshotter and host-gateway probes, therefore still pay for one RPC.
	srv.setID("worker-b")
	c2 := newTestClient(t, lis)
	fresh, err := c2.ListWorkers(ctx)
	require.NoError(t, err)
	require.Equal(t, "worker-b", fresh[0].ID)
	require.Equal(t, 4, srv.calls())
	_, err = c2.ListWorkers(ctx)
	require.NoError(t, err)
	require.Equal(t, 4, srv.calls())

	// The original client is still on the previous connection.
	cached, err := c.ListWorkers(ctx)
	require.NoError(t, err)
	require.Equal(t, "worker-a", cached[0].ID)
	require.Equal(t, 4, srv.calls())

	srv.setID("worker-c")
	lis.closeConns()
	require.Eventually(t, func() bool {
		return c.conn.GetState() != connectivity.Ready
	}, 5*time.Second, 10*time.Millisecond)

	// The old connection's close can overlap the reconnecting RPC, so the first
	// successful response is not always the one that fills the cache. Further
	// calls on the new connection must stop issuing RPCs and must show the
	// post-reconnect worker.
	require.Eventually(t, func() bool {
		got, lerr := c.ListWorkers(ctx)
		if lerr != nil || len(got) != 1 || got[0].ID != "worker-c" {
			return false
		}
		calls := srv.calls()
		_, lerr = c.ListWorkers(ctx)
		return lerr == nil && srv.calls() == calls
	}, 5*time.Second, 20*time.Millisecond)
	require.Greater(t, srv.calls(), 4)
}

func TestBuildReusesListWorkersCache(t *testing.T) {
	srv := &listWorkersServer{id: "worker-a"}
	lis := newTrackingListener(t)
	startControlServer(t, lis, srv)

	ctx, cancel := context.WithTimeoutCause(t.Context(), 20*time.Second, errors.New("build timed out"))
	defer cancel()
	c := newTestClient(t, lis)

	// Same sequence buildx uses: load node capabilities, then build on that client.
	_, err := c.ListWorkers(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, srv.calls())

	build := func() error {
		_, err := c.Build(ctx, SolveOpt{}, "", func(context.Context, gateway.Client) (*gateway.Result, error) {
			return nil, errors.New("stop after worker lookup")
		}, nil)
		return err
	}
	err = build()
	require.Error(t, err)
	require.NotContains(t, err.Error(), "listing workers")
	err = build()
	require.Error(t, err)
	require.NotContains(t, err.Error(), "listing workers")
	require.Equal(t, 1, srv.calls())

	// A client whose first use is Build still lists workers once.
	c2 := newTestClient(t, lis)
	before := srv.calls()
	err = func() error {
		_, err := c2.Build(ctx, SolveOpt{}, "", func(context.Context, gateway.Client) (*gateway.Result, error) {
			return nil, errors.New("stop after worker lookup")
		}, nil)
		return err
	}()
	require.Error(t, err)
	require.NotContains(t, err.Error(), "listing workers")
	err = func() error {
		_, err := c2.Build(ctx, SolveOpt{}, "", func(context.Context, gateway.Client) (*gateway.Result, error) {
			return nil, errors.New("stop after worker lookup")
		}, nil)
		return err
	}()
	require.Error(t, err)
	require.Equal(t, before+1, srv.calls())
}

type listWorkersServer struct {
	controlapi.UnimplementedControlServer
	mu sync.Mutex
	id string
	n  int
}

func (s *listWorkersServer) ListWorkers(context.Context, *controlapi.ListWorkersRequest) (*controlapi.ListWorkersResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	return &controlapi.ListWorkersResponse{Record: []*apitypes.WorkerRecord{{
		ID: s.id,
		Labels: map[string]string{
			"org.mobyproject.buildkit.worker.snapshotter":          "overlayfs",
			"org.mobyproject.buildkit.worker.moby.host-gateway-ip": "172.17.0.1",
		},
		Platforms: []*pb.Platform{{OS: "linux", Architecture: "amd64"}},
		BuildkitVersion: &apitypes.BuildkitVersion{
			Package: "github.com/moby/buildkit",
			Version: "v0.0.0-test",
		},
	}}}, nil
}

func (s *listWorkersServer) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n
}

func (s *listWorkersServer) setID(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.id = id
}

type trackingListener struct {
	net.Listener
	mu    sync.Mutex
	conns []net.Conn
}

func newTrackingListener(t *testing.T) *trackingListener {
	t.Helper()
	var lc net.ListenConfig
	lis, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { lis.Close() })
	return &trackingListener{Listener: lis}
}

func (l *trackingListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	l.conns = append(l.conns, conn)
	l.mu.Unlock()
	return conn, nil
}

func (l *trackingListener) closeConns() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, conn := range l.conns {
		conn.Close()
	}
	l.conns = nil
}

func startControlServer(t *testing.T, lis net.Listener, srv controlapi.ControlServer) {
	t.Helper()
	server := grpc.NewServer()
	controlapi.RegisterControlServer(server, srv)
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(lis) }()
	t.Cleanup(func() {
		server.Stop()
		if err := <-serveErr; err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			require.NoError(t, err)
		}
	})
}

func newTestClient(t *testing.T, lis net.Listener) *Client {
	t.Helper()
	c, err := New(t.Context(), "tcp://"+lis.Addr().String())
	require.NoError(t, err)
	t.Cleanup(func() { c.Close() })
	return c
}
