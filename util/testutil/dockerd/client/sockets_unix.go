//go:build !windows

package client

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"syscall"
	"time"
)

const maxUnixSocketPathSize = len(syscall.RawSockaddrUnix{}.Path)

func configureUnixTransport(tr *http.Transport, proto, addr string) error {
	if len(addr) > maxUnixSocketPathSize {
		return fmt.Errorf("unix socket path %q is too long", addr)
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
func DialPipe(_ string, _ time.Duration) (net.Conn, error) {
	return nil, syscall.EAFNOSUPPORT
}
