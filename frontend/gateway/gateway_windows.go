//go:build windows

package gateway

import (
	"context"
	"net"
	"os"

	"github.com/Microsoft/go-winio"
	"github.com/moby/buildkit/util/appdefaults"
	"github.com/moby/buildkit/util/bklog"
	"github.com/pkg/errors"
)

func createNPipeListener(frontendID string) net.Listener {
	os.Setenv("BUILDKIT_FRONTEND_ID", frontendID)
	pipeCfg := &winio.PipeConfig{
		SecurityDescriptor: "D:P(A;;GA;;;AU)",
		MessageMode:        false,
		InputBufferSize:    4096,
		OutputBufferSize:   4096,
	}
	listener, err := winio.ListenPipe(appdefaults.FrontendGRPCPipe+frontendID, pipeCfg)
	if err != nil {
		bklog.L.Errorf("Failed to initialize named pipe listener: %s", err)
		return nil
	}
	return listener
}

// handleWindowsPipeConn waits for a client to connect to the named pipe.
// It assigns the connection to lbf.conn or cancels the context on error or timeout.
func handleWindowsPipeConn(ctx context.Context, listener net.Listener, lbf *llbBridgeForwarder, cancel context.CancelCauseFunc) error {
	connCh := make(chan net.Conn, 1)
	errCh := make(chan error, 1)

	go func() {
		defer func() {
			if r := recover(); r != nil {
				errCh <- errors.Errorf("panic in Accept: %v", r)
			}
		}()
		conn, err := listener.Accept()
		if err != nil {
			errCh <- err
			return
		}
		connCh <- conn
	}()

	select {
	case <-ctx.Done():
		_ = listener.Close()
		return context.Cause(ctx)
	case err := <-errCh:
		lbf.isErrServerClosed = true
		cancel(errors.WithStack(err))
		return err
	case conn := <-connCh:
		lbf.conn = conn
		return nil
	}
}
