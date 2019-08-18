// +build !dfrunsecurity

package dockerfile2llb

import (
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/frontend/dockerfile/instructions"
	"github.com/moby/buildkit/solver/pb"
)

func dispatchRunSecurity(c *instructions.RunCommand) (llb.RunOption, error) {
	return llb.Security(pb.SecurityMode_SANDBOX), nil
}
