package executor

import (
	"context"

	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/bklog"
	"github.com/moby/buildkit/util/progress"
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

func hasLinuxResources(res *pb.LinuxResources) bool {
	if res == nil {
		return false
	}
	return res.Memory != 0 || res.MemorySwap != 0 ||
		res.CpuShares != 0 || res.CpuPeriod != 0 || res.CpuQuota != 0 ||
		res.CpusetCpus != "" || res.CpusetMems != ""
}
