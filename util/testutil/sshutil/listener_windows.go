package sshutil

import (
	"net"
	"testing"

	"github.com/Microsoft/go-winio"
	"github.com/moby/buildkit/identity"
)

func listen(t *testing.T) (net.Listener, error) {
	t.Helper()
	return winio.ListenPipe(`\\.\pipe\buildkit-test-ssh-`+identity.NewID(), nil)
}
