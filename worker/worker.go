package worker

import (
	"github.com/tonistiigi/buildkit_poc/cache"
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

type Worker interface {
	// TODO: add stdout/err
	Exec(ctx context.Context, meta Meta, mounts map[string]cache.Mountable) error
}
