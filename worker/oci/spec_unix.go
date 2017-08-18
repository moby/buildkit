// +build !windows

package oci

import (
	"context"
	"path"

	"github.com/containerd/containerd"
	"github.com/containerd/containerd/mount"
	"github.com/mitchellh/hashstructure"
	"github.com/moby/buildkit/snapshot"
	"github.com/moby/buildkit/worker"
	specs "github.com/opencontainers/runtime-spec/specs-go"
	"github.com/pkg/errors"
)

// Ideally we don't have to import whole containerd just for the default spec

func GenerateSpec(ctx context.Context, meta worker.Meta, mounts []worker.Mount) (*specs.Spec, func(), error) {
	s, err := containerd.GenerateSpec(
		containerd.WithHostNamespace(specs.NetworkNamespace),
		containerd.WithHostResolvconf,
		containerd.WithHostHostsFile,
	)
	if err != nil {
		return nil, nil, err
	}
	s.Process.Args = meta.Args
	s.Process.Env = meta.Env
	s.Process.Cwd = meta.Cwd
	// TODO: User

	sm := &submounts{}

	for _, m := range mounts {
		mounts, err := m.Src.Mount(ctx, m.Readonly)
		if err != nil {
			sm.cleanup()
			return nil, nil, errors.Wrapf(err, "failed to mount %s", m.Dest)
		}
		for _, mount := range mounts {
			mount, err = sm.subMount(mount, m.Selector)
			if err != nil {
				sm.cleanup()
				return nil, nil, err
			}
			s.Mounts = append(s.Mounts, specs.Mount{
				Destination: m.Dest,
				Type:        mount.Type,
				Source:      mount.Source,
				Options:     mount.Options,
			})
		}
	}

	return s, sm.cleanup, nil
}

type mountRef struct {
	mount   mount.Mount
	unmount func() error
}

type submounts struct {
	m map[uint64]mountRef
}

func (s *submounts) subMount(m mount.Mount, subPath string) (mount.Mount, error) {
	if path.Join("/", subPath) == "/" {
		return m, nil
	}
	if s.m == nil {
		s.m = map[uint64]mountRef{}
	}
	h, err := hashstructure.Hash(m, nil)
	if err != nil {
		return mount.Mount{}, nil
	}
	if mr, ok := s.m[h]; ok {
		return sub(mr.mount, subPath), nil
	}

	lm := snapshot.LocalMounter([]mount.Mount{m})

	mp, err := lm.Mount()
	if err != nil {
		return mount.Mount{}, err
	}

	opts := []string{"rbind"}
	for _, opt := range m.Options {
		if opt == "ro" {
			opts = append(opts, opt)
		}
	}

	s.m[h] = mountRef{
		mount: mount.Mount{
			Source:  mp,
			Type:    "bind",
			Options: opts,
		},
		unmount: lm.Unmount,
	}

	return sub(s.m[h].mount, subPath), nil
}

func (s *submounts) cleanup() {
	for _, m := range s.m {
		m.unmount()
	}
}

func sub(m mount.Mount, subPath string) mount.Mount {
	m.Source = path.Join(m.Source, subPath)
	return m
}
