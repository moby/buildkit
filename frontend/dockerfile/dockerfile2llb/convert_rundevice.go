//go:build dfrundevice

package dockerfile2llb

import (
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/frontend/dockerfile/instructions"
)

func dispatchRunDevices(c *instructions.RunCommand) ([]llb.RunOption, error) {
	var out []llb.RunOption
	devices := instructions.GetDevices(c)
	for _, device := range devices {
		out = append(out, llb.AddCDIDevice(device))
	}
	return out, nil
}
