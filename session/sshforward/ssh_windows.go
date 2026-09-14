//go:build windows

package sshforward

import (
	"context"

	"github.com/Microsoft/go-winio"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/session"
	"github.com/pkg/errors"
)

// defaultSSHPipeSecurityDescriptor grants generic read/write to authenticated
// users and SYSTEM. This matches the ACL of the container-facing tracing socket
// pipe (see cmd/buildkitd getLocalListener with an empty descriptor), which is
// what allows the WCOW container process to reach a forwarded pipe. It is
// intentionally broader than the daemon's host-facing control pipe (which
// defaults to Administrators + SYSTEM), because the consumer here is the
// in-container build process rather than a host-side client.
const defaultSSHPipeSecurityDescriptor = "D:P(A;;GRGW;;;AU)(A;;GRGW;;;SY)"

// sshPipeSecurityDescriptor is the SDDL security descriptor applied to the
// forwarded SSH agent named pipe. It defaults to defaultSSHPipeSecurityDescriptor
// and can be overridden once at daemon startup via SetAgentPipeSecurityDescriptor
// so operators who lock down the daemon's security descriptor have that applied
// to the agent pipe as well.
var sshPipeSecurityDescriptor = defaultSSHPipeSecurityDescriptor

// SetAgentPipeSecurityDescriptor overrides the SDDL security descriptor applied
// to forwarded SSH agent named pipes. An empty sd is ignored and the default
// (Authenticated Users + SYSTEM) is retained. It is intended to be called once
// during daemon startup, before any builds run.
//
// NOTE: tightening the descriptor to exclude the account the WCOW container
// process runs as will make the forwarded agent unreachable inside the
// container; that is the operator's explicit choice, mirroring the daemon's
// control pipe.
func SetAgentPipeSecurityDescriptor(sd string) {
	if sd != "" {
		sshPipeSecurityDescriptor = sd
	}
}

// MountSSHSocket exposes the forwarded SSH agent as a Windows named pipe.
//
// Windows OpenSSH connects to the named pipe `\\.\pipe\openssh-ssh-agent`
// rather than a UNIX socket referenced by SSH_AUTH_SOCK, so the returned path
// is a pipe that is later mounted into the container at that well-known
// location. UID/GID/Mode are POSIX concepts and are ignored on Windows; pipe
// access is governed by the security descriptor instead.
func MountSSHSocket(ctx context.Context, c session.Caller, opt SocketOpt) (sockPath string, closer func() error, err error) {
	pipePath := `\\.\pipe\buildkit-ssh-` + identity.NewID()

	l, err := winio.ListenPipe(pipePath, &winio.PipeConfig{
		SecurityDescriptor: sshPipeSecurityDescriptor,
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
