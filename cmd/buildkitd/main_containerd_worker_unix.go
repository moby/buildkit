//go:build !windows

package main

import (
	runcoptions "github.com/containerd/containerd/api/types/runc/options"
	runtimeoptions "github.com/containerd/containerd/api/types/runtimeoptions/v1"
	"github.com/containerd/containerd/v2/plugins"
)

// getRuntimeOptionsType gets empty runtime options by the runtime type name.
func getRuntimeOptionsType(t string) interface{} {
	if t == plugins.RuntimeRuncV2 {
		return &runcoptions.Options{}
	}
	return &runtimeoptions.Options{}
}
