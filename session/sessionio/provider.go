package sessionio

import (
	"github.com/moby/buildkit/session"
	"google.golang.org/grpc"
)

// NewProvider provides session.Attachable which forwards IO on the specified session.
func NewProvider(initIOFn InitIOFn) session.Attachable {
	return &provider{IOServer: NewIOServer(initIOFn)}
}

type provider struct {
	*IOServer
}

func (p *provider) Register(server *grpc.Server) {
	RegisterIOForwarderServer(server, p)
}
