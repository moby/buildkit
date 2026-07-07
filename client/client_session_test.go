package client

import (
	"context"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/session"
	sessionsecrets "github.com/moby/buildkit/session/secrets"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

func testSessionHealthMonitorFailsBlockedSecretResponse(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows", "Windows secret mounts are covered separately")

	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	st := llb.Image("busybox:latest").
		Run(llb.Shlex(`sh -c '[ "$(cat /run/secrets/mysecret)" = "foo-secret" ]'`), llb.AddSecret("/run/secrets/mysecret"))

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	provider := &gatedSecretProvider{
		t:             t,
		id:            "/run/secrets/mysecret",
		data:          []byte("foo-secret"),
		started:       make(chan struct{}),
		allowResponse: make(chan struct{}),
	}

	baseDialer := c.Dialer()
	proxyCh := make(chan *gatedSessionTunnelProxy, 1)
	c.sessionDialer = func(ctx context.Context, proto string, meta map[string][]string) (net.Conn, error) {
		meta["X-Buildkit-Session-Health-Custom-Timeout"] = []string{"2000"}

		conn, err := baseDialer(ctx, proto, meta)
		if err != nil {
			return nil, err
		}
		proxy, clientConn := newGatedSessionTunnelProxy(conn)
		proxyCh <- proxy
		return clientConn, nil
	}

	done := make(chan error, 1)
	go func() {
		_, err := c.Solve(sb.Context(), def, SolveOpt{
			Session: []session.Attachable{provider},
		}, nil)
		done <- err
	}()

	var stalledConn *gatedSessionTunnelProxy
	select {
	case stalledConn = <-proxyCh:
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for session connection")
	}

	select {
	case <-provider.started:
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for secret request")
	}

	stalledConn.blockClientToDaemon.Store(true)
	close(provider.allowResponse)

	select {
	case <-stalledConn.blockedClientToDaemon:
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for blocked session traffic")
	}

	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for solve to fail after session healthcheck timeout")
	}
}

type gatedSecretProvider struct {
	sessionsecrets.UnimplementedSecretsServer

	t             *testing.T
	id            string
	data          []byte
	started       chan struct{}
	allowResponse chan struct{}
}

func (p *gatedSecretProvider) Register(server *grpc.Server) {
	sessionsecrets.RegisterSecretsServer(server, p)
}

func (p *gatedSecretProvider) GetSecret(ctx context.Context, req *sessionsecrets.GetSecretRequest) (*sessionsecrets.GetSecretResponse, error) {
	require.Equal(p.t, p.id, req.ID)
	select {
	case <-p.started:
	default:
		close(p.started)
	}

	select {
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	case <-p.allowResponse:
		return &sessionsecrets.GetSecretResponse{Data: p.data}, nil
	}
}

type gatedSessionTunnelProxy struct {
	clientConn net.Conn
	proxyConn  net.Conn
	daemonConn net.Conn

	blockClientToDaemon   atomic.Bool
	blockedClientToDaemon chan struct{}
	releaseClientToDaemon chan struct{}
	closed                chan struct{}
	blockedOnce           sync.Once
	releaseOnce           sync.Once
	closeOnce             sync.Once
}

func (p *gatedSessionTunnelProxy) Close() error {
	p.closeOnce.Do(func() {
		close(p.closed)
		_ = p.proxyConn.Close()
		_ = p.clientConn.Close()
		_ = p.daemonConn.Close()
	})
	return nil
}

func (p *gatedSessionTunnelProxy) Release() {
	p.releaseOnce.Do(func() {
		close(p.releaseClientToDaemon)
	})
}

func (p *gatedSessionTunnelProxy) copyClientToDaemon() {
	defer p.Close()

	buf := make([]byte, 32*1024)
	for {
		n, err := p.proxyConn.Read(buf)
		if n > 0 {
			data := append([]byte(nil), buf[:n]...)
			if p.blockClientToDaemon.Load() {
				p.blockedOnce.Do(func() {
					close(p.blockedClientToDaemon)
				})
				select {
				case <-p.releaseClientToDaemon:
				case <-p.closed:
					return
				}
			}
			if _, writeErr := p.daemonConn.Write(data); writeErr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func (p *gatedSessionTunnelProxy) copyDaemonToClient() {
	defer p.Close()
	_, _ = io.Copy(p.proxyConn, p.daemonConn)
}

func newGatedSessionTunnelProxy(daemonConn net.Conn) (*gatedSessionTunnelProxy, net.Conn) {
	clientConn, proxyConn := net.Pipe()
	p := &gatedSessionTunnelProxy{
		clientConn:            clientConn,
		proxyConn:             proxyConn,
		daemonConn:            daemonConn,
		blockedClientToDaemon: make(chan struct{}),
		releaseClientToDaemon: make(chan struct{}),
		closed:                make(chan struct{}),
	}
	go p.copyClientToDaemon()
	go p.copyDaemonToClient()
	return p, clientConn
}
