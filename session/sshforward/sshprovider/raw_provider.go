package sshprovider

import (
	"context"
	"net"

	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/sshforward"
	"github.com/pkg/errors"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

type rawConfig struct {
	ID     string
	Dialer func(context.Context) (net.Conn, error)
}

func newRawProvider(confs []rawConfig) (session.Attachable, error) {
	m := make(map[string]func(context.Context) (net.Conn, error), len(confs))

	for _, conf := range confs {
		if conf.ID == "" {
			conf.ID = sshforward.DefaultID
		}
		_, ok := m[conf.ID]
		if ok {
			return nil, errors.Errorf("invalid duplicate ID %s", conf.ID)
		}
		m[conf.ID] = conf.Dialer
	}

	return &rawProvider{m: m}, nil
}

type rawProvider struct {
	m map[string]func(context.Context) (net.Conn, error)
}

func (p *rawProvider) CheckAgent(ctx context.Context, req *sshforward.CheckAgentRequest) (*sshforward.CheckAgentResponse, error) {
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

func (p *rawProvider) ForwardAgent(stream sshforward.SSH_ForwardAgentServer) error {
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

func (p *rawProvider) Register(srv *grpc.Server) {
	sshforward.RegisterSSHServer(srv, p)
}
