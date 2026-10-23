package executor

import (
	"context"

	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/bklog"
	"github.com/moby/buildkit/util/progress"
	"google.golang.org/protobuf/reflect/protoreflect"
)

const linuxResourcesIgnoredMsg = "resource limits are ignored by the rootless worker"

// WarnLinuxResourcesIgnored reports that the resource limits requested for a
// step were not applied. Rootless executors call it because they run steps
// without a cgroup. The warning goes to the daemon log and to the build
// output of the step that ctx belongs to.
func WarnLinuxResourcesIgnored(ctx context.Context, res *pb.LinuxResources) {
	if !hasLinuxResources(res) {
		return
	}
	bklog.G(ctx).Warn(linuxResourcesIgnoredMsg)
	pw, ok, _ := progress.NewFromContext(ctx)
	if !ok {
		return
	}
	pw.Write(identity.NewID(), client.VertexWarning{
		Level: 1,
		Short: []byte(linuxResourcesIgnoredMsg),
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
