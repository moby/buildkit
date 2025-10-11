//go:build !windows

package client

import (
	"context"
	"net"
	"net/http"
	"syscall"

	"github.com/pkg/errors"
)

const maxUnixSocketPathSize = len(syscall.RawSockaddrUnix{}.Path)

func configureUnixTransport(tr *http.Transport, proto, addr string) error {
	if len(addr) > maxUnixSocketPathSize {
		return errors.Errorf("unix socket path %q is too long", addr)
	}
	// No need for compression in local communications.
	tr.DisableCompression = true
	dialer := &net.Dialer{
		Timeout: defaultTimeout,
	}
	tr.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, proto, addr)
	}
	return nil
}

func configureNpipeTransport(_ *http.Transport, _, _ string) error {
	return errors.New("protocol not available")
}

// DialPipe connects to a Windows named pipe.
// This is not supported on other OSes.
func DialPipe(ctx context.Context, addr string) (net.Conn, error) {
	return nil, syscall.EAFNOSUPPORT
}
