package client

import (
	"context"
	"net"
	"net/http"

	"github.com/Microsoft/go-winio"
	"github.com/pkg/errors"
)

func configureUnixTransport(_ *http.Transport, _, _ string) error {
	return errors.New("protocol not available")
}

func configureNpipeTransport(tr *http.Transport, _, addr string) error {
	// No need for compression in local communications.
	tr.DisableCompression = true
	tr.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
		return winio.DialPipeContext(ctx, addr)
	}
	return nil
}

// DialPipe connects to a Windows named pipe.
func DialPipe(ctx context.Context, addr string) (net.Conn, error) {
	return winio.DialPipeContext(ctx, addr)
}
