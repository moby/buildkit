package executor

import (
	"testing"

	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/progress"
	"github.com/stretchr/testify/require"
)

func TestWarnLinuxResourcesIgnored(t *testing.T) {
	for _, tc := range []struct {
		name string
		res  *pb.LinuxResources
		warn bool
	}{
		{name: "nil", res: nil},
		{name: "empty", res: &pb.LinuxResources{}},
		{name: "memory", res: &pb.LinuxResources{Memory: 64 << 20}, warn: true},
		{name: "cpu quota", res: &pb.LinuxResources{CpuQuota: 50000, CpuPeriod: 100000}, warn: true},
		{name: "cpuset", res: &pb.LinuxResources{CpusetCpus: "0"}, warn: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pr, ctx, cancel := progress.NewContext(t.Context())
			WarnLinuxResourcesIgnored(ctx, tc.res)
			cancel(nil)

			var warnings []client.VertexWarning
			for {
				ps, err := pr.Read(t.Context())
				if err != nil || len(ps) == 0 {
					break
				}
				for _, p := range ps {
					if w, ok := p.Sys.(client.VertexWarning); ok {
						warnings = append(warnings, w)
					}
				}
			}
			if !tc.warn {
				require.Empty(t, warnings)
				return
			}
			require.Len(t, warnings, 1)
			require.Equal(t, linuxResourcesIgnoredMsg, string(warnings[0].Short))
		})
	}
}
