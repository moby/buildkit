package sshprovider

import (
	"context"
	"net"

	"github.com/moby/buildkit/session/sshforward"
	"github.com/pkg/errors"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

type dialerFn func(ctx context.Context) (net.Conn, error)

type socketProvider struct {
	m map[string]dialerFn
}

func (p *socketProvider) CheckAgent(ctx context.Context, req *sshforward.CheckAgentRequest) (*sshforward.CheckAgentResponse, error) {
	id := sshforward.DefaultID
	if req.ID != "" {
		id = req.ID
	}

	_, ok := p.m[id]
	if !ok {
		return nil, errors.Errorf("unset ssh forward key %s", id)
	}
	return &sshforward.CheckAgentResponse{}, nil
}

func (p *socketProvider) ForwardAgent(stream sshforward.SSH_ForwardAgentServer) error {
	id := sshforward.DefaultID

	ctx := stream.Context()
	opts, _ := metadata.FromIncomingContext(ctx)

	if v, ok := opts[sshforward.KeySSHID]; ok && len(v) > 0 && v[0] != "" {
		id = v[0]
	}

	dialer, ok := p.m[id]
	if !ok {
		return errors.Errorf("unset ssh forward key %s", id)
	}

	conn, err := dialer(ctx)
	if err != nil {
		return errors.Wrapf(err, "failed to dial agent %s", id)
	}
	defer conn.Close()

	return sshforward.Copy(ctx, conn, stream, nil)
}

func (p *socketProvider) Register(srv *grpc.Server) {
	sshforward.RegisterSSHServer(srv, p)
}
