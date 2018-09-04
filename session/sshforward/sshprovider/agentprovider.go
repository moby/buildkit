package sshprovider

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"time"

	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/sshforward"
	"github.com/pkg/errors"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

type AgentConfig struct {
	ID     string
	Socket string
}

func NewSSHAgentProvider(confs []AgentConfig) (session.Attachable, error) {
	m := map[string]string{}
	for _, conf := range confs {
		if conf.Socket == "" {
			conf.Socket = os.Getenv("SSH_AUTH_SOCK")
		}

		if conf.Socket == "" {
			return nil, errors.Errorf("invalid empty ssh agent socket, make sure SSH_AUTH_SOCK is set")
		}

		if err := validateSSHAgentSocket(conf.Socket); err != nil {
			return nil, err
		}
		if conf.ID == "" {
			conf.ID = sshforward.DefaultID
		}
		if _, ok := m[conf.ID]; ok {
			return nil, errors.Errorf("invalid duplicate ID %s", conf.ID)
		}
		m[conf.ID] = conf.Socket
	}

	return &socketProvider{m: m}, nil
}

type socketProvider struct {
	m map[string]string
}

func (sp *socketProvider) Register(server *grpc.Server) {
	sshforward.RegisterSSHServer(server, sp)
}

func (sp *socketProvider) CheckAgent(ctx context.Context, req *sshforward.CheckAgentRequest) (*sshforward.CheckAgentResponse, error) {
	id := sshforward.DefaultID
	if req.ID != "" {
		id = req.ID
	}
	if _, ok := sp.m[id]; !ok {
		return &sshforward.CheckAgentResponse{}, errors.Errorf("unset ssh forward key %s", id)
	}
	return &sshforward.CheckAgentResponse{}, nil
}

func (sp *socketProvider) ForwardAgent(stream sshforward.SSH_ForwardAgentServer) error {
	id := sshforward.DefaultID

	opts, _ := metadata.FromIncomingContext(stream.Context()) // if no metadata continue with empty object

	if v, ok := opts[sshforward.KeySSHID]; ok && len(v) > 0 && v[0] != "" {
		id = v[0]
	}

	socket, ok := sp.m[id]
	if !ok {
		fmt.Printf("unset11 %s\n", id)
		return errors.Errorf("unset ssh forward key %s", id)
	}

	conn, err := net.DialTimeout("unix", socket, time.Second)
	if err != nil {
		return errors.Wrapf(err, "failed to connect to %s", socket)
	}
	s1, s2 := sockPair()
	a := &readOnlyAgent{agent.NewClient(conn)}

	defer conn.Close()

	eg, ctx := errgroup.WithContext(context.TODO())

	eg.Go(func() error {
		return agent.ServeAgent(a, s1)
	})

	eg.Go(func() error {
		defer s1.Close()
		return sshforward.Copy(ctx, s2, stream)
	})

	return eg.Wait()
}

func validateSSHAgentSocket(socket string) error {
	conn, err := net.DialTimeout("unix", socket, time.Second)
	if err != nil {
		return errors.Wrapf(err, "failed to connect to %s", socket)
	}
	defer conn.Close()
	if _, err := agent.NewClient(conn).List(); err != nil {
		return errors.Wrapf(err, "failed to verify %s as ssh agent socket", socket)
	}
	return nil
}

func sockPair() (io.ReadWriteCloser, io.ReadWriteCloser) {
	pr1, pw1 := io.Pipe()
	pr2, pw2 := io.Pipe()
	return &sock{pr1, pw2, pw1}, &sock{pr2, pw1, pw2}
}

type sock struct {
	io.Reader
	io.Writer
	io.Closer
}

type readOnlyAgent struct {
	agent.Agent
}

func (a *readOnlyAgent) Add(_ agent.AddedKey) error {
	return errors.Errorf("adding new keys not allowed by buildkit")
}

func (a *readOnlyAgent) Remove(_ ssh.PublicKey) error {
	return errors.Errorf("removing keys not allowed by buildkit")
}

func (a *readOnlyAgent) RemoveAll() error {
	return errors.Errorf("removing keys not allowed by buildkit")
}

func (a *readOnlyAgent) Lock(_ []byte) error {
	return errors.Errorf("locking agent not allowed by buildkit")
}
