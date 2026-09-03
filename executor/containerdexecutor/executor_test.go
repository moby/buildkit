package containerdexecutor

import (
	"testing"

	ctd "github.com/containerd/containerd/v2/client"
	gatewayapi "github.com/moby/buildkit/frontend/gateway/pb"
	"github.com/stretchr/testify/require"
)

func TestContainerdUnknownExitStatus(t *testing.T) {
	// There are assumptions in the containerd executor that the UnknownExitStatus
	// used in errdefs.ExitError matches the variable in the containerd package.
	require.Equalf(t, ctd.UnknownExitStatus, gatewayapi.UnknownExitStatus, "containerd.UnknownExitStatus != errdefs.UnknownExitStatus")
}
