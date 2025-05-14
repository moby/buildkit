package cdidevices

import (
	"testing"

	"github.com/moby/buildkit/solver/pb"
	"github.com/stretchr/testify/require"
	"tags.cncf.io/container-device-interface/pkg/cdi"
)

func TestFindDevices(t *testing.T) {
	tests := []struct {
		name     string
		devices  []*pb.CDIDevice
		expected []string
		err      bool
	}{
		{
			name: "Find specific device",
			devices: []*pb.CDIDevice{
				{Name: "vendor1.com/device=foo"},
			},
			expected: []string{"vendor1.com/device=foo"},
		},
		{
			name: "Find first devices",
			devices: []*pb.CDIDevice{
				{Name: "vendor1.com/devicemulti"},
			},
			expected: []string{"vendor1.com/devicemulti=bar"},
		},
		{
			name: "Find all devices of a kind",
			devices: []*pb.CDIDevice{
				{Name: "vendor1.com/devicemulti=*"},
			},
			expected: []string{"vendor1.com/devicemulti=foo", "vendor1.com/devicemulti=bar", "vendor1.com/devicemulti=baz", "vendor1.com/devicemulti=qux"},
		},
		{
			name: "Find devices by class",
			devices: []*pb.CDIDevice{
				{Name: "class1"},
			},
			expected: []string{"vendor1.com/deviceclass=foo", "vendor1.com/deviceclass=bar", "vendor1.com/devicemulti=baz"},
		},
		{
			name: "Device not found",
			devices: []*pb.CDIDevice{
				{Name: "vendor1.com/device=unknown"},
			},
			expected: nil,
			err:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cache, err := cdi.NewCache(cdi.WithSpecDirs("./fixtures"))
			require.NoError(t, err)
			manager := NewManager(cache, nil)
			result, err := manager.FindDevices(tt.devices...)
			if tt.err {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.ElementsMatch(t, tt.expected, result)
			}
		})
	}
}
