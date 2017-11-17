package grpchijack

import (
	"net"

	controlapi "github.com/moby/buildkit/api/services/control"
	"google.golang.org/grpc/metadata"
)

func Hijack(stream controlapi.Control_SessionServer) (net.Conn, map[string][]string) {
	md, _ := metadata.FromIncomingContext(stream.Context())
	return streamToConn(stream), md
}
