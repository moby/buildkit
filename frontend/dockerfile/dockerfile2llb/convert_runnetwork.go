// +build dfrunnetwork

package dockerfile2llb

import (
	"github.com/pkg/errors"

	"github.com/moby/buildkit/frontend/dockerfile/instructions"
	"github.com/moby/buildkit/solver/pb"
)

func dispatchRunNetwork(d *dispatchState, c *instructions.RunCommand) error {
	network := instructions.GetNetwork(c)

	switch network {
	case instructions.NetworkDefault:
		d.state = d.state.Network(pb.NetMode_UNSET)
	case instructions.NetworkNone:
		d.state = d.state.Network(pb.NetMode_NONE)
	case instructions.NetworkHost:
		d.state = d.state.Network(pb.NetMode_HOST)
	default:
		return errors.Errorf("unsupported network mode %q", network)
	}

	return nil
}
