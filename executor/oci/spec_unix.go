// +build !windows

package oci

import (
	"context"
	"path"
	"sync"

	"github.com/containerd/containerd/containers"
	"github.com/containerd/containerd/mount"
	"github.com/containerd/containerd/namespaces"
	"github.com/containerd/containerd/oci"
	"github.com/mitchellh/hashstructure"
	"github.com/moby/buildkit/executor"
	"github.com/moby/buildkit/snapshot"
	specs "github.com/opencontainers/runtime-spec/specs-go"
	"github.com/pkg/errors"
)

// Ideally we don't have to import whole containerd just for the default spec

// GenerateSpec generates spec using containerd functionality.
func GenerateSpec(ctx context.Context, meta executor.Meta, mounts []executor.Mount, id, resolvConf, hostsFile string, hostNetwork bool, opts ...oci.SpecOpts) (*specs.Spec, func(), error) {
	c := &containers.Container{
		ID: id,
	}
	_, ok := namespaces.Namespace(ctx)
	if !ok {
		ctx = namespaces.WithNamespace(ctx, "buildkit")
	}
	// Note that containerd.GenerateSpec is namespaced so as to make
	// specs.Linux.CgroupsPath namespaced
	if hostNetwork {
		opts = append(opts, oci.WithHostNamespace(specs.NetworkNamespace),
			withROBind(resolvConf, "/etc/resolv.conf"),
			withROBind(hostsFile, "/etc/hosts"))
	} else {
		opts = append(opts,
			withROBind(resolvConf, "/etc/resolv.conf"),
			withROBind(hostsFile, "/etc/hosts"))
	}

	s, err := oci.GenerateSpec(ctx, nil, c, opts...)

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

func withROBind(src, dest string) func(_ context.Context, _ oci.Client, _ *containers.Container, s *specs.Spec) error {
	return func(_ context.Context, _ oci.Client, _ *containers.Container, s *specs.Spec) error {
		s.Mounts = append(s.Mounts, specs.Mount{
			Destination: dest,
			Type:        "bind",
			Source:      src,
			Options:     []string{"rbind", "ro"},
		})
		return nil
	}
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
	var wg sync.WaitGroup
	wg.Add(len(s.m))
	for _, m := range s.m {
		func(m mountRef) {
			go func() {
				m.unmount()
				wg.Done()
			}()
		}(m)
	}
	wg.Wait()
}

func sub(m mount.Mount, subPath string) mount.Mount {
	m.Source = path.Join(m.Source, subPath)
	return m
}
