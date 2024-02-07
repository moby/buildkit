package containerdexecutor

import (
	"testing"

	containerd "github.com/containerd/containerd/v2/client"
	gatewayapi "github.com/moby/buildkit/frontend/gateway/pb"
)

func TestContainerdUnknownExitStatus(t *testing.T) {
	// There are assumptions in the containerd executor that the UnknownExitStatus
	// used in errdefs.ExitError matches the variable in the containerd package.
	if containerd.UnknownExitStatus != gatewayapi.UnknownExitStatus {
		t.Fatalf("containerd.UnknownExitStatus != errdefs.UnknownExitStatus")
	}
}
