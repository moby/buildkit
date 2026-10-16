//go:build windows

package sshforward

import (
	"context"
	"fmt"

	"github.com/Microsoft/go-winio"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/session"
	"github.com/pkg/errors"
	"golang.org/x/sys/windows"
)

const windowsContainerGroupSID = "S-1-5-93-0"

func sshPipeSecurityDescriptor() (string, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", errors.Wrap(err, "getting current process token user")
	}
	return fmt.Sprintf("D:P(A;;GRGW;;;%s)(A;;GRGW;;;%s)", user.User.Sid, windowsContainerGroupSID), nil
}

// MountSSHSocket exposes the forwarded SSH agent as a Windows named pipe.
//
// UID/GID/Mode are POSIX concepts and are ignored on Windows; pipe access is
// governed by the security descriptor instead.
func MountSSHSocket(ctx context.Context, c session.Caller, opt SocketOpt) (sockPath string, closer func() error, err error) {
	pipePath := `\\.\pipe\buildkit-ssh-` + identity.NewID()

	securityDescriptor, err := sshPipeSecurityDescriptor()
	if err != nil {
		return "", nil, err
	}
	l, err := winio.ListenPipe(pipePath, &winio.PipeConfig{
		SecurityDescriptor: securityDescriptor,
	})
	if err != nil {
		return "", nil, errors.WithStack(err)
	}

	s := &server{caller: c}

	id := opt.ID
	if id == "" {
		id = DefaultID
	}

	go s.run(ctx, l, id) // erroring per connection allowed

	return pipePath, func() error {
		return errors.WithStack(l.Close())
	}, nil
}
