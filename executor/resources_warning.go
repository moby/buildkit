package executor

import (
	"context"
	"slices"

	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/bklog"
	"github.com/moby/buildkit/util/progress"
	"github.com/opencontainers/runtime-spec/specs-go"
	"google.golang.org/protobuf/reflect/protoreflect"
)

const (
	linuxResourcesIgnoredMsg = "resource limits are ignored by the rootless worker"
	cpusetIgnoredMsg         = "cpuset limits are ignored because the cpuset cgroup controller is not delegated to the rootless worker"
	cgroupParentIgnoredMsg   = "the rootless worker ignores the cgroup parent of the step and runs it in its own cgroup"
)

// WarnLinuxResourcesIgnored reports that the resource limits requested for a
// step were not applied. Rootless executors call it when they run a step
// without a cgroup. The warning goes to the build output of the step that ctx
// belongs to.
func WarnLinuxResourcesIgnored(ctx context.Context, res *pb.LinuxResources) {
	if !hasLinuxResources(res) {
		return
	}
	warn(ctx, linuxResourcesIgnoredMsg)
}

// WarnCpusetIgnored reports that the cpuset limits of a step were not applied
// while its other limits were.
func WarnCpusetIgnored(ctx context.Context) {
	warn(ctx, cpusetIgnoredMsg)
}

// WarnCgroupParentIgnored reports that a step runs in the cgroup of the
// rootless worker instead of the cgroup parent it asked for.
func WarnCgroupParentIgnored(ctx context.Context) {
	warn(ctx, cgroupParentIgnoredMsg)
}

// warn sends msg to the build output of the step. The daemon log only gets
// it at debug level, since it can repeat for every step of every build.
func warn(ctx context.Context, msg string) {
	bklog.G(ctx).Debug(msg)
	pw, ok, _ := progress.NewFromContext(ctx)
	if !ok {
		return
	}
	pw.Write(identity.NewID(), client.VertexWarning{
		Level: 1,
		Short: []byte(msg),
	})
	pw.Close()
}

// hasLinuxResources reports whether any limit is set in res. Range only visits
// fields with a non-zero value, so new fields are covered without listing them
// here.
func hasLinuxResources(res *pb.LinuxResources) bool {
	if res == nil {
		return false
	}
	set := false
	res.ProtoReflect().Range(func(protoreflect.FieldDescriptor, protoreflect.Value) bool {
		set = true
		return false
	})
	return set
}

// DropUnsupportedCpuset removes the cpuset limits from spec when the cpuset
// controller is missing from controllers, because runc cannot start the
// container otherwise. systemd delegates cpuset to user services only when
// configured to.
func DropUnsupportedCpuset(ctx context.Context, spec *specs.Spec, controllers []string) {
	if slices.Contains(controllers, "cpuset") {
		return
	}
	r := spec.Linux.Resources
	if r == nil || r.CPU == nil || (r.CPU.Cpus == "" && r.CPU.Mems == "") {
		return
	}
	r.CPU.Cpus = ""
	r.CPU.Mems = ""
	WarnCpusetIgnored(ctx)
}
