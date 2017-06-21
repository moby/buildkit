package oci

import (
	"context"

	"github.com/containerd/containerd"
	specs "github.com/opencontainers/runtime-spec/specs-go"
	"github.com/pkg/errors"
	"github.com/tonistiigi/buildkit_poc/cache"
	"github.com/tonistiigi/buildkit_poc/worker"
)

// Ideally we don't have to import whole containerd just for the default spec

func GenerateSpec(ctx context.Context, meta worker.Meta, mounts map[string]cache.Mountable) (*specs.Spec, error) {
	s, err := containerd.GenerateSpec(containerd.WithHostNamespace(specs.NetworkNamespace))
	if err != nil {
		return nil, err
	}
	s.Process.Args = meta.Args
	s.Process.Env = meta.Env
	s.Process.Cwd = meta.Cwd
	// TODO: User

	for dest, m := range mounts {
		if dest == "/" {
			continue
		}
		mounts, err := m.Mount(ctx)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to mount to %s", dest)
		}
		for _, mount := range mounts {
			s.Mounts = append(s.Mounts, specs.Mount{
				Destination: dest,
				Type:        mount.Type,
				Source:      mount.Source,
				Options:     mount.Options,
			})
		}
	}

	return s, nil
}
