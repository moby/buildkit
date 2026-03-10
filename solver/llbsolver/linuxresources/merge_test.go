package linuxresources

import (
	"testing"

	"github.com/moby/buildkit/solver/pb"
	"github.com/stretchr/testify/require"
)

func TestMergeRelaxed(t *testing.T) {
	t.Parallel()

	t.Run("both nil", func(t *testing.T) {
		result := mergeRelaxed(nil, nil)
		require.Nil(t, result)
	})

	t.Run("first nil", func(t *testing.T) {
		b := &pb.LinuxResources{Memory: 64 * 1024 * 1024}
		result := mergeRelaxed(nil, b)
		require.Equal(t, b, result)
	})

	t.Run("second nil", func(t *testing.T) {
		a := &pb.LinuxResources{Memory: 64 * 1024 * 1024}
		result := mergeRelaxed(a, nil)
		require.Equal(t, a, result)
	})

	t.Run("higher memory wins", func(t *testing.T) {
		a := &pb.LinuxResources{Memory: 64 * 1024 * 1024}
		b := &pb.LinuxResources{Memory: 128 * 1024 * 1024}
		result := mergeRelaxed(a, b)
		require.Equal(t, int64(128*1024*1024), result.Memory)
	})

	t.Run("zero memory means unlimited and wins", func(t *testing.T) {
		a := &pb.LinuxResources{Memory: 64 * 1024 * 1024}
		b := &pb.LinuxResources{Memory: 0}
		result := mergeRelaxed(a, b)
		require.Equal(t, int64(0), result.Memory)

		// Reverse order should also work
		result = mergeRelaxed(b, a)
		require.Equal(t, int64(0), result.Memory)
	})

	t.Run("zero memory swap means unset, preserves non-zero", func(t *testing.T) {
		a := &pb.LinuxResources{MemorySwap: 256 * 1024 * 1024}
		b := &pb.LinuxResources{MemorySwap: 0}
		result := mergeRelaxed(a, b)
		require.Equal(t, int64(256*1024*1024), result.MemorySwap)

		// Reverse order
		result = mergeRelaxed(b, a)
		require.Equal(t, int64(256*1024*1024), result.MemorySwap)
	})

	t.Run("memory swap -1 means unlimited and wins", func(t *testing.T) {
		a := &pb.LinuxResources{MemorySwap: 256 * 1024 * 1024}
		b := &pb.LinuxResources{MemorySwap: -1}
		result := mergeRelaxed(a, b)
		require.Equal(t, int64(-1), result.MemorySwap)

		// Reverse order
		result = mergeRelaxed(b, a)
		require.Equal(t, int64(-1), result.MemorySwap)
	})

	t.Run("higher memory swap wins", func(t *testing.T) {
		a := &pb.LinuxResources{MemorySwap: 128 * 1024 * 1024}
		b := &pb.LinuxResources{MemorySwap: 256 * 1024 * 1024}
		result := mergeRelaxed(a, b)
		require.Equal(t, int64(256*1024*1024), result.MemorySwap)
	})

	t.Run("higher cpu shares wins", func(t *testing.T) {
		a := &pb.LinuxResources{CpuShares: 256}
		b := &pb.LinuxResources{CpuShares: 512}
		result := mergeRelaxed(a, b)
		require.Equal(t, uint64(512), result.CpuShares)
	})

	t.Run("zero cpu shares means unset, preserves non-zero", func(t *testing.T) {
		a := &pb.LinuxResources{CpuShares: 512}
		b := &pb.LinuxResources{CpuShares: 0}
		result := mergeRelaxed(a, b)
		require.Equal(t, uint64(512), result.CpuShares)

		// Reverse order
		result = mergeRelaxed(b, a)
		require.Equal(t, uint64(512), result.CpuShares)
	})

	t.Run("higher cpu quota wins", func(t *testing.T) {
		a := &pb.LinuxResources{CpuQuota: 50000, CpuPeriod: 100000}
		b := &pb.LinuxResources{CpuQuota: 80000, CpuPeriod: 100000}
		result := mergeRelaxed(a, b)
		require.Equal(t, int64(80000), result.CpuQuota)
		require.Equal(t, uint64(100000), result.CpuPeriod)
	})

	t.Run("zero cpu quota means unlimited and wins", func(t *testing.T) {
		a := &pb.LinuxResources{CpuQuota: 50000, CpuPeriod: 100000}
		b := &pb.LinuxResources{CpuQuota: 0, CpuPeriod: 100000}
		result := mergeRelaxed(a, b)
		require.Equal(t, int64(0), result.CpuQuota)
	})

	t.Run("cpu bandwidth picks pair with higher cap, not mixed", func(t *testing.T) {
		// cap A = 50000/100000 = 0.5; cap B = 80000/200000 = 0.4 — A wins as a pair.
		a := &pb.LinuxResources{CpuQuota: 50000, CpuPeriod: 100000}
		b := &pb.LinuxResources{CpuQuota: 80000, CpuPeriod: 200000}
		result := mergeRelaxed(a, b)
		require.Equal(t, int64(50000), result.CpuQuota)
		require.Equal(t, uint64(100000), result.CpuPeriod)
	})

	t.Run("cpu bandwidth: higher cap wins as a pair", func(t *testing.T) {
		// cap A = 80000/100000 = 0.8; cap B = 50000/100000 = 0.5 — A wins.
		a := &pb.LinuxResources{CpuQuota: 80000, CpuPeriod: 100000}
		b := &pb.LinuxResources{CpuQuota: 50000, CpuPeriod: 100000}
		result := mergeRelaxed(a, b)
		require.Equal(t, int64(80000), result.CpuQuota)
		require.Equal(t, uint64(100000), result.CpuPeriod)
	})

	t.Run("equal quotas picks shorter period (more relaxed)", func(t *testing.T) {
		a := &pb.LinuxResources{CpuQuota: 50000, CpuPeriod: 100000}
		b := &pb.LinuxResources{CpuQuota: 50000, CpuPeriod: 200000}
		result := mergeRelaxed(a, b)
		require.Equal(t, int64(50000), result.CpuQuota)
		require.Equal(t, uint64(100000), result.CpuPeriod) // shorter period = more CPU
	})

	t.Run("empty cpuset string means unset and wins", func(t *testing.T) {
		a := &pb.LinuxResources{CpusetCpus: "0-3"}
		b := &pb.LinuxResources{CpusetCpus: ""}
		result := mergeRelaxed(a, b)
		require.Equal(t, "", result.CpusetCpus)
	})

	t.Run("cpuset subset is absorbed by superset", func(t *testing.T) {
		a := &pb.LinuxResources{CpusetCpus: "0-3"}
		b := &pb.LinuxResources{CpusetCpus: "0-7"}
		result := mergeRelaxed(a, b)
		require.Equal(t, "0-7", result.CpusetCpus)
	})

	t.Run("cpuset range and list merge into union", func(t *testing.T) {
		a := &pb.LinuxResources{CpusetCpus: "0-7"}
		b := &pb.LinuxResources{CpusetCpus: "0,1,2"}
		result := mergeRelaxed(a, b)
		require.Equal(t, "0-7", result.CpusetCpus)
	})

	t.Run("disjoint cpusets produce true union", func(t *testing.T) {
		a := &pb.LinuxResources{CpusetCpus: "0-1"}
		b := &pb.LinuxResources{CpusetCpus: "2-3"}
		result := mergeRelaxed(a, b)
		require.Equal(t, "0-3", result.CpusetCpus)
	})

	t.Run("cpuset union collapses adjacent ranges", func(t *testing.T) {
		a := &pb.LinuxResources{CpusetCpus: "0,2,4"}
		b := &pb.LinuxResources{CpusetCpus: "1,3,5"}
		result := mergeRelaxed(a, b)
		require.Equal(t, "0-5", result.CpusetCpus)
	})

	t.Run("cpuset union keeps gaps as separate ranges", func(t *testing.T) {
		a := &pb.LinuxResources{CpusetCpus: "0-2,7"}
		b := &pb.LinuxResources{CpusetCpus: "5,9"}
		result := mergeRelaxed(a, b)
		require.Equal(t, "0-2,5,7,9", result.CpusetCpus)
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
		result := mergeRelaxed(a, b)
		require.Equal(t, int64(128*1024*1024), result.Memory)
		require.Equal(t, int64(128*1024*1024), result.MemorySwap)
		require.Equal(t, uint64(512), result.CpuShares)
		// CPU bandwidth is picked as a pair (not mixed). cap A = 50000/100000 =
		// 0.5; cap B = 80000/200000 = 0.4 — pair A wins.
		require.Equal(t, int64(50000), result.CpuQuota)
		require.Equal(t, uint64(100000), result.CpuPeriod)
		require.Equal(t, "0-3", result.CpusetCpus) // union of "0-3" and "0-1"
		require.Equal(t, "0-1", result.CpusetMems) // union of "0" and "0,1"
	})

	t.Run("no pointer aliasing when one side is nil", func(t *testing.T) {
		b := &pb.LinuxResources{Memory: 64 * 1024 * 1024}
		result := mergeRelaxed(nil, b)
		require.NotSame(t, b, result, "result should be a clone, not the same pointer")
		require.Equal(t, b.Memory, result.Memory)

		a := &pb.LinuxResources{Memory: 128 * 1024 * 1024}
		result = mergeRelaxed(a, nil)
		require.NotSame(t, a, result, "result should be a clone, not the same pointer")
		require.Equal(t, a.Memory, result.Memory)
	})
}
