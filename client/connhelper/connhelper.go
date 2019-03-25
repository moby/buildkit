// Package connhelper provides helpers for connecting to a remote daemon host with custom logic.
package connhelper

import (
	"context"
	"net"
	"net/url"

	"github.com/docker/cli/cli/connhelper/commandconn"
)

// ConnectionHelper allows to connect to a remote host with custom stream provider binary.
type ConnectionHelper struct {
	// ContextDialer can be passed to grpc.WithContextDialer
	ContextDialer func(ctx context.Context, addr string) (net.Conn, error)
}

// GetConnectionHelper returns BuildKit-specific connection helper for the given URL.
// GetConnectionHelper returns nil without error when no helper is registered for the scheme.
//
// docker://<container> URL requires BuildKit v0.5.0 or later in the container.
func GetConnectionHelper(daemonURL string) (*ConnectionHelper, error) {
	u, err := url.Parse(daemonURL)
	if err != nil {
		return nil, err
	}
	switch scheme := u.Scheme; scheme {
	case "docker":
		container := u.Host
		return &ConnectionHelper{
			ContextDialer: func(ctx context.Context, addr string) (net.Conn, error) {
				return commandconn.New(ctx, "docker", "exec", "-i", container, "buildctl", "dial-stdio")
			},
		}, nil
	}
	return nil, err
}
