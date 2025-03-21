//go:build !windows

package gateway

import (
	"context"
	"net"
)

func createNPipeListener() net.Listener {
	return nil
}

func handleWindowsPipeConn(_ context.Context, _ net.Listener, _ *llbBridgeForwarder, _ context.CancelCauseFunc) error {
	return nil
}
