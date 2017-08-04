// +build !windows

package oci

import (
	"context"

	"github.com/containerd/containerd"
	"github.com/moby/buildkit/worker"
	specs "github.com/opencontainers/runtime-spec/specs-go"
	"github.com/pkg/errors"
)

// Ideally we don't have to import whole containerd just for the default spec

func GenerateSpec(ctx context.Context, meta worker.Meta, mounts []worker.Mount) (*specs.Spec, error) {
	s, err := containerd.GenerateSpec(containerd.WithHostNamespace(specs.NetworkNamespace))
	if err != nil {
		return nil, err
	}
	s.Process.Args = meta.Args
	s.Process.Env = meta.Env
	s.Process.Cwd = meta.Cwd
	// TODO: User

	for _, m := range mounts {
		mounts, err := m.Src.Mount(ctx, m.Readonly)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to mount %s", m.Dest)
		}
		for _, mount := range mounts {
			s.Mounts = append(s.Mounts, specs.Mount{
				Destination: m.Dest,
				Type:        mount.Type,
				Source:      mount.Source,
				Options:     mount.Options,
			})
		}
	}

	return s, nil
}
