//go:build windows

package grpcclient

import (
	"context"
	"net"
	"os"

	"github.com/Microsoft/go-winio"
	"github.com/moby/buildkit/util/appdefaults"
	"github.com/pkg/errors"
	"google.golang.org/grpc"
)

func nPipeDialer() (string, grpc.DialOption) {
	addr := appdefaults.FrontendGRPCPipe + os.Getenv("BUILDKIT_FRONTEND_ID")
	dialFn := func(ctx context.Context, _ string) (net.Conn, error) {
		conn, err := winio.DialPipe(addr, nil)
		if err != nil {
			return nil, errors.Wrap(err, "Failed to connect to gRPC server")
		}
		return conn, nil
	}
	return addr, grpc.WithContextDialer(dialFn)
}
