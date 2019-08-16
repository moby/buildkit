// +build !dfrunnetwork

package dockerfile2llb

import (
	"github.com/moby/buildkit/frontend/dockerfile/instructions"
)

func dispatchRunNetwork(d *dispatchState, c *instructions.RunCommand) error {
	return nil
}
