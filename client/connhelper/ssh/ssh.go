// Package ssh provides connhelper for ssh://<SSH URL>
package ssh

import (
	"context"
	"net"
	"net/url"

	"github.com/docker/cli/cli/connhelper/commandconn"
	"github.com/moby/buildkit/client/connhelper"
	"github.com/pkg/errors"
)

func init() {
	connhelper.Register("ssh", Helper)
}

// Helper returns helper for connecting through an SSH URL.
func Helper(u *url.URL) (*connhelper.ConnectionHelper, error) {
	sp, err := SpecFromURL(u)
	if err != nil {
		return nil, err
	}
	return &connhelper.ConnectionHelper{
		ContextDialer: func(ctx context.Context, addr string) (net.Conn, error) {
			ctxFlags := []string{}
			if sp.User != "" {
				ctxFlags = append(ctxFlags, "-l", sp.User)
			}
			if sp.Port != "" {
				ctxFlags = append(ctxFlags, "-p", sp.Port)
			}
			ctxFlags = append(ctxFlags, "--", sp.Host)
			// using background context because context remains active for the duration of the process, after dial has completed
			return commandconn.New(context.Background(), "ssh", append(ctxFlags, []string{"buildctl", "dial-stdio"}...)...)
		},
	}, nil
}

// Spec
type Spec struct {
	User string
	Host string
	Port string
}

// SpecFromURL creates Spec from URL.
// URL is like ssh://<user>@host:<port>
// Only <host> part is mandatory.
func SpecFromURL(u *url.URL) (*Spec, error) {
	sp := Spec{
		Host: u.Hostname(),
		Port: u.Port(),
	}
	if user := u.User; user != nil {
		sp.User = user.Username()
		if _, ok := user.Password(); ok {
			return nil, errors.New("plain-text password is not supported")
		}
	}
	if sp.Host == "" {
		return nil, errors.Errorf("no host specified")
	}
	if u.Path != "" {
		return nil, errors.Errorf("extra path after the host: %q", u.Path)
	}
	if u.RawQuery != "" {
		return nil, errors.Errorf("extra query after the host: %q", u.RawQuery)
	}
	if u.Fragment != "" {
		return nil, errors.Errorf("extra fragment after the host: %q", u.Fragment)
	}
	return &sp, nil
}
