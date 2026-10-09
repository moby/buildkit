//go:build !windows

package sshforward

import (
	"context"
	"net"
	"os"
	"path/filepath"

	"github.com/moby/buildkit/session"
	"github.com/pkg/errors"
)

func MountSSHSocket(ctx context.Context, c session.Caller, opt SocketOpt) (sockPath string, closer func() error, err error) {
	dir, err := os.MkdirTemp("", ".buildkit-ssh-sock")
	if err != nil {
		return "", nil, errors.WithStack(err)
	}

	defer func() {
		if err != nil {
			os.RemoveAll(dir)
		}
	}()

	if err := os.Chmod(dir, 0711); err != nil {
		return "", nil, errors.WithStack(err)
	}

	sockPath = filepath.Join(dir, "ssh_auth_sock")

	listener := net.ListenConfig{}
	l, err := listener.Listen(context.TODO(), "unix", sockPath)
	if err != nil {
		return "", nil, errors.WithStack(err)
	}

	if err := os.Chown(sockPath, opt.UID, opt.GID); err != nil {
		l.Close()
		return "", nil, errors.WithStack(err)
	}
	if err := os.Chmod(sockPath, os.FileMode(opt.Mode)); err != nil {
		l.Close()
		return "", nil, errors.WithStack(err)
	}

	s := &server{caller: c}

	id := opt.ID
	if id == "" {
		id = DefaultID
	}

	go s.run(ctx, l, id) // erroring per connection allowed

	return sockPath, func() error {
		err := l.Close()
		os.RemoveAll(sockPath)
		return errors.WithStack(err)
	}, nil
}
