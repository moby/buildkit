package llb

import (
	"context"
	"testing"

	"github.com/moby/buildkit/solver/pb"
	"github.com/stretchr/testify/require"
)

func TestTmpfsMountError(t *testing.T) {
	t.Parallel()

	st := Image("foo").Run(Shlex("args")).AddMount("/tmp", Scratch(), Tmpfs())
	_, err := st.Marshal(context.TODO())

	require.Error(t, err)
	require.Contains(t, err.Error(), "can't be used as a parent")

	st = Image("foo").Run(Shlex("args"), AddMount("/tmp", Scratch(), Tmpfs())).Root()
	_, err = st.Marshal(context.TODO())
	require.NoError(t, err)

	st = Image("foo").Run(Shlex("args"), AddMount("/tmp", Image("bar"), Tmpfs())).Root()
	_, err = st.Marshal(context.TODO())
	require.Error(t, err)
	require.Contains(t, err.Error(), "must use scratch")
}

func TestValidGetMountIndex(t *testing.T) {
	// tests for https://github.com/moby/buildkit/issues/1520

	// tmpfs mount /c will sort later than target mount /b, /b will have output index==1
	st := Image("foo").Run(Shlex("args"), AddMount("/b", Scratch()), AddMount("/c", Scratch(), Tmpfs())).GetMount("/b")

	mountOutput, ok := st.Output().(*output)
	require.True(t, ok, "mount output is expected type")

	mountIndex, err := mountOutput.getIndex()
	require.NoError(t, err, "failed to getIndex")
	require.Equal(t, pb.OutputIndex(1), mountIndex, "unexpected mount index")

	// now swapping so the tmpfs mount /a will sort earlier than the target mount /b, /b should still have output index==1
	st = Image("foo").Run(Shlex("args"), AddMount("/b", Scratch()), AddMount("/a", Scratch(), Tmpfs())).GetMount("/b")

	mountOutput, ok = st.Output().(*output)
	require.True(t, ok, "mount output is expected type")

	mountIndex, err = mountOutput.getIndex()
	require.NoError(t, err, "failed to getIndex")
	require.Equal(t, pb.OutputIndex(1), mountIndex, "unexpected mount index")
}

func TestLinuxResourcesMarshal(t *testing.T) {
	t.Parallel()

	st := Image("busybox:latest").
		Run(
			Shlex("true"),
			MemoryLimit(64*1024*1024),
			CPUShares(512),
			CPUQuota(50000),
			CPUPeriod(100000),
			CPUsetCPUs("0-3"),
			CPUsetMems("0"),
		).Root()

	def, err := st.Marshal(context.TODO())
	require.NoError(t, err)

	// Resources should be in OpMetadata (not in the Op bytes / cache key)
	var found bool
	for _, md := range def.Metadata {
		if md.LinuxResources == nil {
			continue
		}
		found = true
		res := md.LinuxResources
		require.Equal(t, int64(64*1024*1024), res.Memory)
		require.Equal(t, uint64(512), res.CpuShares)
		require.Equal(t, int64(50000), res.CpuQuota)
		require.Equal(t, uint64(100000), res.CpuPeriod)
		require.Equal(t, "0-3", res.CpusetCpus)
		require.Equal(t, "0", res.CpusetMems)
	}
	require.True(t, found, "LinuxResources not found in OpMetadata")
}

func TestLinuxResourcesNotInCacheKey(t *testing.T) {
	t.Parallel()

	// Two ops with same command but different resource limits must produce the same digest
	st1 := Image("busybox:latest").
		Run(Shlex("echo hello"), MemoryLimit(64*1024*1024)).Root()

	st2 := Image("busybox:latest").
		Run(Shlex("echo hello"), MemoryLimit(128*1024*1024)).Root()

	st3 := Image("busybox:latest").
		Run(Shlex("echo hello")).Root()

	def1, err := st1.Marshal(context.TODO())
	require.NoError(t, err)

	def2, err := st2.Marshal(context.TODO())
	require.NoError(t, err)

	def3, err := st3.Marshal(context.TODO())
	require.NoError(t, err)

	// All three should produce the same definition bytes (same cache key)
	require.Equal(t, def1.Def, def2.Def, "different resource limits should produce same digest")
	require.Equal(t, def1.Def, def3.Def, "resource limits vs no limits should produce same digest")
}

func TestLinuxResourcesMerge(t *testing.T) {
	t.Parallel()

	// Test that individual resource limit functions merge correctly
	st := Image("busybox:latest").
		Run(
			Shlex("true"),
			MemoryLimit(64*1024*1024),
			CPUShares(512),
		).Root()

	def, err := st.Marshal(context.TODO())
	require.NoError(t, err)

	for _, md := range def.Metadata {
		if md.LinuxResources == nil {
			continue
		}
		res := md.LinuxResources
		require.Equal(t, int64(64*1024*1024), res.Memory)
		require.Equal(t, uint64(512), res.CpuShares)
		// Unset fields should be zero
		require.Equal(t, int64(0), res.CpuQuota)
		require.Equal(t, uint64(0), res.CpuPeriod)
		return
	}
	t.Fatal("LinuxResources not found in OpMetadata")
}

func TestExecOpMarshalConsistency(t *testing.T) {
	var prevDef [][]byte
	st := Image("busybox:latest").
		Run(
			Args([]string{"ls /a; ls /b"}),
			AddMount("/b", Scratch().File(Mkfile("file1", 0644, []byte("file1 contents")))),
		).AddMount("/a", Scratch().File(Mkfile("file2", 0644, []byte("file2 contents"))))

	for range 100 {
		def, err := st.Marshal(context.TODO())
		require.NoError(t, err)

		if prevDef != nil {
			require.Equal(t, def.Def, prevDef)
		}

		prevDef = def.Def
	}
}
