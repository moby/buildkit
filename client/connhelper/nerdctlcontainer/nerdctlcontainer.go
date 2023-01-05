// Package nerdctlcontainer provides connhelper for nerdctl-container://<container>
package nerdctlcontainer

import (
	"context"
	"net"
	"net/url"

	"github.com/docker/cli/cli/connhelper/commandconn"
	"github.com/moby/buildkit/client/connhelper"
	"github.com/pkg/errors"
)

func init() {
	connhelper.Register("nerdctl-container", Helper)
}

// Helper returns helper for connecting to a Nerdctl container.
// Requires BuildKit v0.5.0 or later in the container.
func Helper(u *url.URL) (*connhelper.ConnectionHelper, error) {
	sp, err := SpecFromURL(u)
	if err != nil {
		return nil, err
	}
	return &connhelper.ConnectionHelper{
		ContextDialer: func(ctx context.Context, addr string) (net.Conn, error) {
			// using background context because context remains active for the duration of the process, after dial has completed
			return commandconn.New(context.Background(), "nerdctl", []string{"exec", "-i", sp.Container, "buildctl", "dial-stdio"}...)
		},
	}, nil
}

// Spec
type Spec struct {
	Container string
}

// SpecFromURL creates Spec from URL.
// URL is like nerdctl-container://<container>
// Only <container> part is mandatory.
func SpecFromURL(u *url.URL) (*Spec, error) {
	sp := Spec{
		Container: u.Hostname(),
	}
	if sp.Container == "" {
		return nil, errors.New("url lacks container name")
	}
	return &sp, nil
}
