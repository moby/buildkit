package solver

import (
	"testing"

	"github.com/moby/buildkit/solver/pb"
	"github.com/stretchr/testify/require"
)

func TestMergeLinuxResourcesRelaxed(t *testing.T) {
	t.Parallel()

	t.Run("both nil", func(t *testing.T) {
		result := mergeLinuxResourcesRelaxed(nil, nil)
		require.Nil(t, result)
	})

	t.Run("first nil", func(t *testing.T) {
		b := &pb.LinuxResources{Memory: 64 * 1024 * 1024}
		result := mergeLinuxResourcesRelaxed(nil, b)
		require.Equal(t, b, result)
	})

	t.Run("second nil", func(t *testing.T) {
		a := &pb.LinuxResources{Memory: 64 * 1024 * 1024}
		result := mergeLinuxResourcesRelaxed(a, nil)
		require.Equal(t, a, result)
	})

	t.Run("higher memory wins", func(t *testing.T) {
		a := &pb.LinuxResources{Memory: 64 * 1024 * 1024}
		b := &pb.LinuxResources{Memory: 128 * 1024 * 1024}
		result := mergeLinuxResourcesRelaxed(a, b)
		require.Equal(t, int64(128*1024*1024), result.Memory)
	})

	t.Run("zero memory means unlimited and wins", func(t *testing.T) {
		a := &pb.LinuxResources{Memory: 64 * 1024 * 1024}
		b := &pb.LinuxResources{Memory: 0}
		result := mergeLinuxResourcesRelaxed(a, b)
		require.Equal(t, int64(0), result.Memory)

		// Reverse order should also work
		result = mergeLinuxResourcesRelaxed(b, a)
		require.Equal(t, int64(0), result.Memory)
	})

	t.Run("zero memory swap means unset, preserves non-zero", func(t *testing.T) {
		a := &pb.LinuxResources{MemorySwap: 256 * 1024 * 1024}
		b := &pb.LinuxResources{MemorySwap: 0}
		result := mergeLinuxResourcesRelaxed(a, b)
		require.Equal(t, int64(256*1024*1024), result.MemorySwap)

		// Reverse order
		result = mergeLinuxResourcesRelaxed(b, a)
		require.Equal(t, int64(256*1024*1024), result.MemorySwap)
	})

	t.Run("memory swap -1 means unlimited and wins", func(t *testing.T) {
		a := &pb.LinuxResources{MemorySwap: 256 * 1024 * 1024}
		b := &pb.LinuxResources{MemorySwap: -1}
		result := mergeLinuxResourcesRelaxed(a, b)
		require.Equal(t, int64(-1), result.MemorySwap)

		// Reverse order
		result = mergeLinuxResourcesRelaxed(b, a)
		require.Equal(t, int64(-1), result.MemorySwap)
	})

	t.Run("higher memory swap wins", func(t *testing.T) {
		a := &pb.LinuxResources{MemorySwap: 128 * 1024 * 1024}
		b := &pb.LinuxResources{MemorySwap: 256 * 1024 * 1024}
		result := mergeLinuxResourcesRelaxed(a, b)
		require.Equal(t, int64(256*1024*1024), result.MemorySwap)
	})

	t.Run("higher cpu shares wins", func(t *testing.T) {
		a := &pb.LinuxResources{CpuShares: 256}
		b := &pb.LinuxResources{CpuShares: 512}
		result := mergeLinuxResourcesRelaxed(a, b)
		require.Equal(t, uint64(512), result.CpuShares)
	})

	t.Run("zero cpu shares means unset, preserves non-zero", func(t *testing.T) {
		a := &pb.LinuxResources{CpuShares: 512}
		b := &pb.LinuxResources{CpuShares: 0}
		result := mergeLinuxResourcesRelaxed(a, b)
		require.Equal(t, uint64(512), result.CpuShares)

		// Reverse order
		result = mergeLinuxResourcesRelaxed(b, a)
		require.Equal(t, uint64(512), result.CpuShares)
	})

	t.Run("higher cpu quota wins", func(t *testing.T) {
		a := &pb.LinuxResources{CpuQuota: 50000, CpuPeriod: 100000}
		b := &pb.LinuxResources{CpuQuota: 80000, CpuPeriod: 100000}
		result := mergeLinuxResourcesRelaxed(a, b)
		require.Equal(t, int64(80000), result.CpuQuota)
		require.Equal(t, uint64(100000), result.CpuPeriod)
	})

	t.Run("zero cpu quota means unlimited and wins", func(t *testing.T) {
		a := &pb.LinuxResources{CpuQuota: 50000, CpuPeriod: 100000}
		b := &pb.LinuxResources{CpuQuota: 0, CpuPeriod: 100000}
		result := mergeLinuxResourcesRelaxed(a, b)
		require.Equal(t, int64(0), result.CpuQuota)
	})

	t.Run("cpu period follows more relaxed quota", func(t *testing.T) {
		a := &pb.LinuxResources{CpuQuota: 50000, CpuPeriod: 100000}
		b := &pb.LinuxResources{CpuQuota: 80000, CpuPeriod: 200000}
		result := mergeLinuxResourcesRelaxed(a, b)
		require.Equal(t, int64(80000), result.CpuQuota)
		require.Equal(t, uint64(200000), result.CpuPeriod)
	})

	t.Run("equal quotas picks shorter period (more relaxed)", func(t *testing.T) {
		a := &pb.LinuxResources{CpuQuota: 50000, CpuPeriod: 100000}
		b := &pb.LinuxResources{CpuQuota: 50000, CpuPeriod: 200000}
		result := mergeLinuxResourcesRelaxed(a, b)
		require.Equal(t, int64(50000), result.CpuQuota)
		require.Equal(t, uint64(100000), result.CpuPeriod) // shorter period = more CPU
	})

	t.Run("empty cpuset string means unset and wins", func(t *testing.T) {
		a := &pb.LinuxResources{CpusetCpus: "0-3"}
		b := &pb.LinuxResources{CpusetCpus: ""}
		result := mergeLinuxResourcesRelaxed(a, b)
		require.Equal(t, "", result.CpusetCpus)
	})

	t.Run("cpuset covering more CPUs wins", func(t *testing.T) {
		a := &pb.LinuxResources{CpusetCpus: "0-3"}
		b := &pb.LinuxResources{CpusetCpus: "0-7"}
		result := mergeLinuxResourcesRelaxed(a, b)
		require.Equal(t, "0-7", result.CpusetCpus)
	})

	t.Run("cpuset range beats shorter list with fewer CPUs", func(t *testing.T) {
		a := &pb.LinuxResources{CpusetCpus: "0-7"}
		b := &pb.LinuxResources{CpusetCpus: "0,1,2"}
		result := mergeLinuxResourcesRelaxed(a, b)
		require.Equal(t, "0-7", result.CpusetCpus)
	})

	t.Run("cpuset list beats range with fewer CPUs", func(t *testing.T) {
		a := &pb.LinuxResources{CpusetCpus: "0,1,2,3,4,5,6,7,8,9"}
		b := &pb.LinuxResources{CpusetCpus: "0-3"}
		result := mergeLinuxResourcesRelaxed(a, b)
		require.Equal(t, "0,1,2,3,4,5,6,7,8,9", result.CpusetCpus)
	})

	t.Run("all fields merged independently", func(t *testing.T) {
		a := &pb.LinuxResources{
			Memory:     64 * 1024 * 1024,
			MemorySwap: 128 * 1024 * 1024,
			CpuShares:  512,
			CpuPeriod:  100000,
			CpuQuota:   50000,
			CpusetCpus: "0-3",
			CpusetMems: "0",
		}
		b := &pb.LinuxResources{
			Memory:     128 * 1024 * 1024,
			MemorySwap: 64 * 1024 * 1024,
			CpuShares:  256,
			CpuPeriod:  200000,
			CpuQuota:   80000,
			CpusetCpus: "0-1",
			CpusetMems: "0,1",
		}
		result := mergeLinuxResourcesRelaxed(a, b)
		require.Equal(t, int64(128*1024*1024), result.Memory)
		require.Equal(t, int64(128*1024*1024), result.MemorySwap)
		require.Equal(t, uint64(512), result.CpuShares)
		require.Equal(t, int64(80000), result.CpuQuota)
		require.Equal(t, uint64(200000), result.CpuPeriod) // follows the more relaxed quota (b)
		require.Equal(t, "0-3", result.CpusetCpus)         // more CPUs wins (4 > 2)
		require.Equal(t, "0,1", result.CpusetMems)         // more nodes wins (2 > 1)
	})

	t.Run("no pointer aliasing when one side is nil", func(t *testing.T) {
		b := &pb.LinuxResources{Memory: 64 * 1024 * 1024}
		result := mergeLinuxResourcesRelaxed(nil, b)
		require.NotSame(t, b, result, "result should be a clone, not the same pointer")
		require.Equal(t, b.Memory, result.Memory)

		a := &pb.LinuxResources{Memory: 128 * 1024 * 1024}
		result = mergeLinuxResourcesRelaxed(a, nil)
		require.NotSame(t, a, result, "result should be a clone, not the same pointer")
		require.Equal(t, a.Memory, result.Memory)
	})
}
