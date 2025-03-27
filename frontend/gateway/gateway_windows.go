//go:build windows

package gateway

import (
	"net"

	"github.com/Microsoft/go-winio"
	"github.com/moby/buildkit/util/appdefaults"
	"github.com/moby/buildkit/util/bklog"
)

func createNPipeListener() net.Listener {
	pipeCfg := &winio.PipeConfig{
		SecurityDescriptor: "D:P(A;;GA;;;AU)",
		MessageMode:        false,
		InputBufferSize:    4096,
		OutputBufferSize:   4096,
	}
	listener, err := winio.ListenPipe(appdefaults.FrontendGRPCPipe, pipeCfg)
	if listener != nil {
		bklog.L.Printf("Npipe created successfully:")
	}
	if err != nil {
		bklog.L.Errorf("Failed to initialize named pipe listener: %s", err)
		return nil
	}
	return listener
}
