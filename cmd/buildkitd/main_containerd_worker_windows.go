package main

import (
	runhcsoptions "github.com/Microsoft/hcsshim/cmd/containerd-shim-runhcs-v1/options"
	"github.com/containerd/containerd/api/types/runtimeoptions/v1"
	"github.com/containerd/containerd/v2/plugins"
)

// getRuntimeOptionsType gets empty runtime options by the runtime type name.
func getRuntimeOptionsType(t string) interface{} {
	if t == plugins.RuntimeRunhcsV1 {
		return &runhcsoptions.Options{}
	}
	return &runtimeoptions.Options{}
}
