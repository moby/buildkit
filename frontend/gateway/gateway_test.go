package gateway

import (
	"context"
	"sync"
	"testing"
	"time"

	gwclient "github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/frontend/gateway/container"
	gatewaypb "github.com/moby/buildkit/frontend/gateway/pb"
	spb "github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/grpcerrors"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
)

func TestCheckSourceIsAllowed(t *testing.T) {
	makeGatewayFrontend := func(sources []string) (*gatewayFrontend, error) {
		gw, err := NewGatewayFrontend(nil, sources)
		if err != nil {
			return nil, err
		}
		gw1 := gw.(*gatewayFrontend)
		return gw1, nil
	}

	var gw *gatewayFrontend
	var err error

	// no restrictions
	gw, err = makeGatewayFrontend([]string{})
	require.NoError(t, err)
	err = gw.checkSourceIsAllowed("anything")
	require.NoError(t, err)

	gw, err = makeGatewayFrontend([]string{"docker-registry.wikimedia.org/repos/releng/blubber/buildkit:9.9.9"})
	require.NoError(t, err)
	err = gw.checkSourceIsAllowed("docker-registry.wikimedia.org/repos/releng/blubber/buildkit")
	require.NoError(t, err)
	err = gw.checkSourceIsAllowed("docker-registry.wikimedia.org/repos/releng/blubber/buildkit:v1.2.3")
	require.NoError(t, err)
	err = gw.checkSourceIsAllowed("docker-registry.wikimedia.org/something-else")
	require.Error(t, err)

	gw, err = makeGatewayFrontend([]string{"alpine"})
	require.NoError(t, err)
	err = gw.checkSourceIsAllowed("alpine")
	require.NoError(t, err)
	err = gw.checkSourceIsAllowed("library/alpine")
	require.NoError(t, err)
	err = gw.checkSourceIsAllowed("docker.io/library/alpine")
	require.NoError(t, err)
}

func TestReturnRequiresResult(t *testing.T) {
	_, err := (&llbBridgeForwarder{}).Return(t.Context(), &gatewaypb.ReturnRequest{})
	require.Equal(t, codes.InvalidArgument, grpcerrors.Code(err))
	require.ErrorContains(t, err, "result is required")
}

func TestNewContainerValidatesMounts(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mounts  []*spb.Mount
		wantErr string
	}{
		{
			name:    "missing root",
			wantErr: "root mount is required",
		},
		{
			name: "missing SSH options",
			mounts: []*spb.Mount{
				{Dest: spb.RootMount, MountType: spb.MountType_BIND},
				{Dest: "/run/buildkit/ssh_agent.0", MountType: spb.MountType_SSH},
			},
			wantErr: "SSH mount \"/run/buildkit/ssh_agent.0\" requires options",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := (&llbBridgeForwarder{}).NewContainer(t.Context(), &gatewaypb.NewContainerRequest{
				ContainerID: "container",
				Mounts:      tc.mounts,
			})
			require.Equal(t, codes.InvalidArgument, grpcerrors.Code(err))
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}

func TestDiscardWaitsForContainerCreation(t *testing.T) {
	containerCtx, cancelContainerCtx := context.WithCancelCause(t.Context())
	creationStarted := make(chan struct{})
	finishCreation := make(chan struct{})
	var finishOnce sync.Once
	finish := func() { finishOnce.Do(func() { close(finishCreation) }) }
	t.Cleanup(finish)
	ctr := &gatewayLifecycleTestContainer{released: make(chan struct{})}
	lbf := &llbBridgeForwarder{
		containerCtx:       containerCtx,
		cancelContainerCtx: cancelContainerCtx,
		ctrs:               map[string]gwclient.Container{},
		newContainer: func(context.Context, container.NewContainerRequest) (gwclient.Container, error) {
			close(creationStarted)
			<-finishCreation
			return ctr, nil
		},
	}

	creationDone := make(chan error, 1)
	go func() {
		_, err := lbf.NewContainer(t.Context(), gatewayLifecycleContainerRequest("container"))
		creationDone <- err
	}()
	receiveGatewayLifecycleTest(t, creationStarted, "container creation")

	discardDone := make(chan struct{})
	go func() {
		lbf.Discard()
		close(discardDone)
	}()

	require.Eventually(t, func() bool {
		lbf.mu.Lock()
		defer lbf.mu.Unlock()
		return lbf.closing
	}, 5*time.Second, 10*time.Millisecond)
	select {
	case <-discardDone:
		t.Fatal("discard completed while container creation was in progress")
	default:
	}

	finish()
	require.NoError(t, receiveGatewayLifecycleTest(t, creationDone, "container creation completion"))
	receiveGatewayLifecycleTest(t, ctr.released, "container release")
	receiveGatewayLifecycleTest(t, discardDone, "discard")

	_, err := lbf.NewContainer(t.Context(), gatewayLifecycleContainerRequest("rejected"))
	require.Equal(t, codes.Unavailable, grpcerrors.Code(err))
	require.ErrorContains(t, err, "gateway forwarder is closing")
}

func TestDiscardCancelsContainerCreation(t *testing.T) {
	containerCtx, cancelContainerCtx := context.WithCancelCause(t.Context())
	creationStarted := make(chan struct{})
	contextAtStart := make(chan error, 1)
	lbf := &llbBridgeForwarder{
		containerCtx:       containerCtx,
		cancelContainerCtx: cancelContainerCtx,
		ctrs:               map[string]gwclient.Container{},
		newContainer: func(ctx context.Context, _ container.NewContainerRequest) (gwclient.Container, error) {
			close(creationStarted)
			contextAtStart <- context.Cause(ctx)
			<-ctx.Done()
			return nil, context.Cause(ctx)
		},
	}

	creationDone := make(chan error, 1)
	go func() {
		_, err := lbf.NewContainer(t.Context(), gatewayLifecycleContainerRequest("container"))
		creationDone <- err
	}()
	receiveGatewayLifecycleTest(t, creationStarted, "container creation")
	require.NoError(t, receiveGatewayLifecycleTest(t, contextAtStart, "live container context"))

	discardDone := make(chan struct{})
	go func() {
		lbf.Discard()
		close(discardDone)
	}()
	require.ErrorIs(t, receiveGatewayLifecycleTest(t, creationDone, "container creation cancellation"), context.Canceled)
	receiveGatewayLifecycleTest(t, discardDone, "discard")
}

type gatewayLifecycleTestContainer struct {
	gwclient.Container
	released chan struct{}
	once     sync.Once
}

func (c *gatewayLifecycleTestContainer) Release(context.Context) error {
	c.once.Do(func() { close(c.released) })
	return nil
}

func gatewayLifecycleContainerRequest(id string) *gatewaypb.NewContainerRequest {
	return &gatewaypb.NewContainerRequest{
		ContainerID: id,
		Mounts: []*spb.Mount{
			{Dest: spb.RootMount, MountType: spb.MountType_BIND},
		},
	}
}

func receiveGatewayLifecycleTest[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		var zero T
		return zero
	}
}
