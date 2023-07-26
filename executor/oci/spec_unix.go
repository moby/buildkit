//go:build !windows
// +build !windows

package oci

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/container-orchestrated-devices/container-device-interface/pkg/cdi"
	"github.com/containerd/containerd/containers"
	"github.com/containerd/containerd/oci"
	cdseccomp "github.com/containerd/containerd/pkg/seccomp"
	"github.com/docker/docker/pkg/idtools"
	"github.com/docker/docker/profiles/seccomp"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/bklog"
	"github.com/moby/buildkit/util/entitlements/security"
	specs "github.com/opencontainers/runtime-spec/specs-go"
	selinux "github.com/opencontainers/selinux/go-selinux"
	"github.com/opencontainers/selinux/go-selinux/label"
	"github.com/pkg/errors"
)

var (
	cgroupNSOnce     sync.Once
	supportsCgroupNS bool
)

const (
	tracingSocketPath = "/dev/otel-grpc.sock"
)

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
		opts = append(opts, func(_ context.Context, _ oci.Client, _ *containers.Container, s *oci.Spec) error {
			var err error
			if selinuxB {
				s.Process.SelinuxLabel, s.Linux.MountLabel, err = label.InitLabels(nil)
			}
			return err
		})
		return opts, nil
	}
	return nil, nil
}

// generateProcessModeOpts may affect mounts, so must be called after generateMountOpts
func generateProcessModeOpts(mode ProcessMode) ([]oci.SpecOpts, error) {
	if mode == NoProcessSandbox {
		return []oci.SpecOpts{
			oci.WithHostNamespace(specs.PIDNamespace),
			withBoundProc(),
		}, nil
		// TODO(AkihiroSuda): Configure seccomp to disable ptrace (and prctl?) explicitly
	}
	return nil, nil
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

// genereateCDIOptions creates the OCI runtime spec options for injecting CDI
// devices. Two options are returned: The first ensures that the CDI registry
// is initialized with refresh disabled, and the second injects the devices
// into the container.
func generateCDIOpts(devices []*pb.CDIDevice) ([]oci.SpecOpts, error) {
	if len(devices) == 0 {
		return nil, nil
	}
	var dd []string
	for _, d := range devices {
		if d == nil {
			continue
		}
		dd = append(dd, d.Name)
	}

	// Inits the CDI registry and disables auto-refresh.
	withStaticCDIRegistry := func() oci.SpecOpts {
		return func(ctx context.Context, _ oci.Client, _ *containers.Container, s *oci.Spec) error {
			registry := cdi.GetRegistry(cdi.WithAutoRefresh(false))
			if err := registry.Refresh(); err != nil {
				// We don't consider registry refresh failure a fatal error.
				// For instance, a dynamically generated invalid CDI Spec file for
				// any particular vendor shouldn't prevent injection of devices of
				// different vendors. CDI itself knows better, and it will fail the
				// injection if necessary.
				bklog.G(ctx).Warnf("CDI registry refresh failed: %v", err)
			}
			return nil
		}
	}

	return []oci.SpecOpts{
		withStaticCDIRegistry(),
		oci.WithCDIDevices(dd...),
	}, nil
}
