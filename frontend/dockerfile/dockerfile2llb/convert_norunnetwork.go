// +build !dfrunnetwork

package dockerfile2llb

import (
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/frontend/dockerfile/instructions"
	"github.com/moby/buildkit/solver/pb"
)

func dispatchRunNetwork(c *instructions.RunCommand) (llb.RunOption, error) {
	return llb.Network(pb.NetMode_UNSET), nil
}
