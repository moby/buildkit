package executor

import (
	"context"
	"io"
	"net"

	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/solver/pb"
)

type Meta struct {
	Args           []string
	Env            []string
	User           string
	Cwd            string
	Tty            bool
	ReadonlyRootFS bool
	ExtraHosts     []HostIP
	NetMode        pb.NetMode
	SecurityMode   pb.SecurityMode
}

type Mount struct {
	Src      cache.Mountable
	Selector string
	Dest     string
	Readonly bool
}

type ProcessInfo struct {
	Meta           Meta
	Stdin          io.ReadCloser
	Stdout, Stderr io.WriteCloser
}

type Executor interface {
	Run(ctx context.Context, id string, rootfs cache.Mountable, mounts []Mount, process ProcessInfo) error
	Exec(ctx context.Context, id string, process ProcessInfo) error
}

type HostIP struct {
	Host string
	IP   net.IP
}
