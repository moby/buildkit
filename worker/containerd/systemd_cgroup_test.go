package containerd

import (
	"testing"

	runcoptions "github.com/containerd/containerd/api/types/runc/options"
	runtimeoptions "github.com/containerd/containerd/api/types/runtimeoptions/v1"
	"github.com/stretchr/testify/require"
)

func TestSystemdCgroupDriver(t *testing.T) {
	require.False(t, systemdCgroupDriver(nil))
	require.False(t, systemdCgroupDriver(&RuntimeInfo{Name: "io.containerd.runc.v2", Options: &runcoptions.Options{}}))
	require.False(t, systemdCgroupDriver(&RuntimeInfo{Name: "io.containerd.runc.v2", Options: &runtimeoptions.Options{}}))
	require.True(t, systemdCgroupDriver(&RuntimeInfo{Name: "io.containerd.runc.v2", Options: &runcoptions.Options{SystemdCgroup: true}}))
}
