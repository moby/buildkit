//go:build !windows
// +build !windows

package oci

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/containerd/containerd/containers"
	"github.com/containerd/containerd/oci"
	cdseccomp "github.com/containerd/containerd/pkg/seccomp"
	"github.com/docker/docker/pkg/idtools"
	"github.com/docker/docker/profiles/seccomp"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/entitlements/security"
	specs "github.com/opencontainers/runtime-spec/specs-go"
<<<<<<< HEAD
=======
	selinux "github.com/opencontainers/selinux/go-selinux"
	"github.com/opencontainers/selinux/go-selinux/label"
>>>>>>> origin/v0.10
	"github.com/pkg/errors"
)

var (
	cgroupNSOnce     sync.Once
	supportsCgroupNS bool
)

// GenerateSpec generates spec using containerd functionality.
// opts are ignored for s.Process, s.Hostname, and s.Mounts .
func GenerateSpec(ctx context.Context, meta executor.Meta, mounts []executor.Mount, id, resolvConf, hostsFile string, namespace network.Namespace, processMode ProcessMode, idmap *idtools.IdentityMapping, opts ...oci.SpecOpts) (*specs.Spec, func(), error) {
	c := &containers.Container{
		ID: id,
	}
	_, ok := namespaces.Namespace(ctx)
	if !ok {
		ctx = namespaces.WithNamespace(ctx, "buildkit")
	}
	if meta.SecurityMode == pb.SecurityMode_INSECURE {
		opts = append(opts, entitlements.WithInsecureSpec())
	} else if system.SeccompSupported() && meta.SecurityMode == pb.SecurityMode_SANDBOX {
		opts = append(opts, seccomp.WithDefaultProfile())
	}

func generateMountOpts(resolvConf, hostsFile string) ([]oci.SpecOpts, error) {
	return []oci.SpecOpts{
		// https://github.com/moby/buildkit/issues/429
		withRemovedMount("/run"),
		withROBind(resolvConf, "/etc/resolv.conf"),
		withROBind(hostsFile, "/etc/hosts"),
		withCGroup(),
	}, nil
}

// generateSecurityOpts may affect mounts, so must be called after generateMountOpts
func generateSecurityOpts(mode pb.SecurityMode, apparmorProfile string, selinuxB bool) (opts []oci.SpecOpts, _ error) {
	if selinuxB && !selinux.GetEnabled() {
		return nil, errors.New("selinux is not available")
	}
	switch mode {
	case pb.SecurityMode_INSECURE:
		return []oci.SpecOpts{
			security.WithInsecureSpec(),
			oci.WithWriteableCgroupfs,
			oci.WithWriteableSysfs,
			func(_ context.Context, _ oci.Client, _ *containers.Container, s *oci.Spec) error {
				var err error
				if selinuxB {
					s.Process.SelinuxLabel, s.Linux.MountLabel, err = label.InitLabels([]string{"disable"})
				}
				return err
			},
		}, nil
	case pb.SecurityMode_SANDBOX:
		if cdseccomp.IsEnabled() {
			opts = append(opts, withDefaultProfile())
		}
		if apparmorProfile != "" {
			opts = append(opts, oci.WithApparmorProfile(apparmorProfile))
		}
<<<<<<< HEAD
	}

	for _, m := range mounts {
		if m.Src == nil {
			return nil, nil, errors.Errorf("mount %s has no source", m.Dest)
		}
		mountable, err := m.Src.Mount(ctx, m.Readonly)
		if err != nil {
			releaseAll()
			return nil, nil, errors.Wrapf(err, "failed to mount %s", m.Dest)
		}
		mounts, release, err := mountable.Mount()
		if err != nil {
			releaseAll()
			return nil, nil, errors.WithStack(err)
		}
		releasers = append(releasers, release)
		for _, mount := range mounts {
			mount, err = sm.subMount(mount, m.Selector)
			if err != nil {
				releaseAll()
				return nil, nil, err
=======
		opts = append(opts, func(_ context.Context, _ oci.Client, _ *containers.Container, s *oci.Spec) error {
			var err error
			if selinuxB {
				s.Process.SelinuxLabel, s.Linux.MountLabel, err = label.InitLabels(nil)
>>>>>>> origin/v0.10
			}
			return err
		})
		return opts, nil
	}
	return nil, nil
}

type mountRef struct {
	mount   mount.Mount
	unmount func() error
}

type submounts struct {
	m map[uint64]mountRef
}

func generateIDmapOpts(idmap *idtools.IdentityMapping) ([]oci.SpecOpts, error) {
	if idmap == nil {
		return nil, nil
	}
	return []oci.SpecOpts{
		oci.WithUserNamespace(specMapping(idmap.UIDMaps), specMapping(idmap.GIDMaps)),
	}, nil
}

func generateRlimitOpts(ulimits []*pb.Ulimit) ([]oci.SpecOpts, error) {
	if len(ulimits) == 0 {
		return nil, nil
	}
	var rlimits []specs.POSIXRlimit
	for _, u := range ulimits {
		if u == nil {
			continue
		}
		rlimits = append(rlimits, specs.POSIXRlimit{
			Type: fmt.Sprintf("RLIMIT_%s", strings.ToUpper(u.Name)),
			Hard: uint64(u.Hard),
			Soft: uint64(u.Soft),
		})
	}
	return []oci.SpecOpts{
		func(_ context.Context, _ oci.Client, _ *containers.Container, s *specs.Spec) error {
			s.Process.Rlimits = rlimits
			return nil
		},
	}, nil
}

// withDefaultProfile sets the default seccomp profile to the spec.
// Note: must follow the setting of process capabilities
func withDefaultProfile() oci.SpecOpts {
	return func(_ context.Context, _ oci.Client, _ *containers.Container, s *specs.Spec) error {
		var err error
		s.Linux.Seccomp, err = seccomp.GetDefaultProfile(s)
		return err
	}
}

func getTracingSocketMount(socket string) specs.Mount {
	return specs.Mount{
		Destination: tracingSocketPath,
		Type:        "bind",
		Source:      socket,
		Options:     []string{"ro", "rbind"},
	}
}

func getTracingSocket() string {
	return fmt.Sprintf("unix://%s", tracingSocketPath)
}

func cgroupNamespaceSupported() bool {
	cgroupNSOnce.Do(func() {
		if _, err := os.Stat("/proc/self/ns/cgroup"); !os.IsNotExist(err) {
			supportsCgroupNS = true
		}
	})
	return supportsCgroupNS
}
