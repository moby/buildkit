package gateway

import (
	"testing"

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
