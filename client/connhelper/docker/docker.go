// Package connhelper provides helpers for connecting to a remote daemon host with custom logic.
package connhelper

import (
	"context"
	"net"
	"net/url"

	"github.com/docker/cli/cli/connhelper/commandconn"
	"github.com/moby/buildkit/client/connhelper"
)

func init() {
	connhelper.Register("docker", DockerHelper)
}

// DockerHelper returns helper for connecting to Docker container.
// docker://<container> URL requires BuildKit v0.5.0 or later in the container.
func DockerHelper(u *url.URL) (*connhelper.ConnectionHelper, error) {
	container := u.Host
	return &connhelper.ConnectionHelper{
		ContextDialer: func(ctx context.Context, addr string) (net.Conn, error) {
			return commandconn.New(ctx, "docker", "exec", "-i", container, "buildctl", "dial-stdio")
		},
	}, nil
}
