package sshforward

import (
	"context"
	"net"

	"github.com/moby/buildkit/session"
	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc/metadata"
)

// DefaultID is the default ssh ID
const DefaultID = "default"

const KeySSHID = "buildkit.ssh.id"

type server struct {
	caller session.Caller
}

func (s *server) run(ctx context.Context, l net.Listener, id string) error {
	eg, ctx := errgroup.WithContext(ctx)

	eg.Go(func() error {
		<-ctx.Done()
		return context.Cause(ctx)
	})

	eg.Go(func() error {
		for {
			conn, err := l.Accept()
			if err != nil {
				return err
			}

			client := NewSSHClient(s.caller.Conn())
			rpcCtx := s.caller.Context(ctx)

			opts := make(map[string][]string)
			opts[KeySSHID] = []string{id}
			rpcCtx = metadata.NewOutgoingContext(rpcCtx, opts)

			stream, err := client.ForwardAgent(rpcCtx)
			if err != nil {
				conn.Close()
				return err
			}

			go Copy(rpcCtx, conn, stream, stream.CloseSend)
		}
	})

	return eg.Wait()
}

type SocketOpt struct {
	ID   string
	UID  int
	GID  int
	Mode int
}

func CheckSSHID(ctx context.Context, c session.Caller, id string) error {
	ctx = c.Context(ctx)
	client := NewSSHClient(c.Conn())
	_, err := client.CheckAgent(ctx, &CheckAgentRequest{ID: id})
	return errors.WithStack(err)
}
