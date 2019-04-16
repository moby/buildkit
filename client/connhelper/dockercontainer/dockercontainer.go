// Package dockercontainer provides connhelper for docker-container://<container>
package dockercontainer

import (
	"context"
	"net"
	"net/url"

	"github.com/docker/cli/cli/connhelper/commandconn"
	"github.com/moby/buildkit/client/connhelper"
)

func init() {
	connhelper.Register("docker-container", DockerContainerHelper)
}

// DockerContainerHelper returns helper for connecting to Docker container.
// docker-container://<container> URL requires BuildKit v0.5.0 or later in the container.
func DockerContainerHelper(u *url.URL) (*connhelper.ConnectionHelper, error) {
	container := u.Host
	return &connhelper.ConnectionHelper{
		ContextDialer: func(ctx context.Context, addr string) (net.Conn, error) {
			return commandconn.New(ctx, "docker", "exec", "-i", container, "buildctl", "dial-stdio")
		},
	}, nil
}
