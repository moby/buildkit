package worker

import (
	"io"

	"github.com/moby/buildkit/cache"
	"golang.org/x/net/context"
)

type Meta struct {
	Args []string
	Env  []string
	User string
	Cwd  string
	Tty  bool
	// DisableNetworking bool
}

type Mount struct {
	Src      cache.Mountable
	Dest     string
	Readonly bool
}

type Worker interface {
	// TODO: add stdout/err
	Exec(ctx context.Context, meta Meta, rootfs cache.Mountable, mounts []Mount, stdout, stderr io.WriteCloser) error
}
