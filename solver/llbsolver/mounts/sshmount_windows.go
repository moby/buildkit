//go:build windows

package mounts

import (
	"context"

	"github.com/containerd/containerd/v2/core/mount"
	"github.com/moby/buildkit/session/sshforward"
)

// namedPipeMountType marks the returned mount as a Windows named pipe so the
// executor forwards it straight to HCS (see executor/oci.NamedPipeMountType).
const namedPipeMountType = "npipe"

// Mount exposes the forwarded SSH agent to a WCOW container as a named pipe.
//
// Unlike the Unix implementation there is no UNIX socket, chown or chmod:
// Windows OpenSSH talks to a named pipe and pipe access is governed by its
// security descriptor. UID/GID/Mode from the mount options are ignored.
func (sm *sshMountInstance) Mount() ([]mount.Mount, func() error, error) {
	ctx, cancel := context.WithCancelCause(context.TODO())

	sock, cleanup, err := sshforward.MountSSHSocket(ctx, sm.sm.caller, sshforward.SocketOpt{
		ID: sm.sm.mount.SSHOpt.ID,
	})
	if err != nil {
		cancel(err)
		return nil, nil, err
	}
	release := func() error {
		var err error
		if cleanup != nil {
			err = cleanup()
		}
		cancel(err)
		return err
	}

	return []mount.Mount{{
		Type:   namedPipeMountType,
		Source: sock,
	}}, release, nil
}
