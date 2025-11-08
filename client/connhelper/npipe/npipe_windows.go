//go:build windows

package npipe

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/Microsoft/go-winio"
	"github.com/moby/buildkit/client/connhelper"
)

// Helper returns helper for connecting to a url via npipes.
func Helper(u *url.URL) (*connhelper.ConnectionHelper, error) {
	addrParts := strings.SplitN(u.String(), "://", 2)
	if len(addrParts) != 2 {
		return nil, fmt.Errorf("invalid address %s", u)
	}
	address := strings.ReplaceAll(addrParts[1], "/", "\\")
	return &connhelper.ConnectionHelper{
		ContextDialer: func(ctx context.Context, addr string) (net.Conn, error) {
			return winio.DialPipeContext(ctx, address)
		},
	}, nil
}
