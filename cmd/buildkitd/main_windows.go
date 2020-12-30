// +build windows

package main

import (
	"crypto/tls"
	"net"

	_ "github.com/moby/buildkit/solver/llbsolver/ops"
	"github.com/pkg/errors"
)

func listenFD(addr string, tlsConfig *tls.Config) (net.Listener, error) {
	return nil, errors.New("listening server on fd not supported on windows")
}
