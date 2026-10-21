package cdidevices

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/moby/buildkit/solver/pb"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
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

func TestOnDemandInstallerConcurrentAccess(t *testing.T) {
	dir := t.TempDir()
	cache, err := cdi.NewCache(cdi.WithSpecDirs(dir), cdi.WithAutoRefresh(false))
	require.NoError(t, err)
	manager := NewManager(cache, []string{"configured.test/device=foo"})
	kinds := []string{"concurrent.test/first", "concurrent.test/second"}
	ready := make(chan struct{}, len(kinds))
	start := make(chan struct{})
	var installs []func(context.Context) error
	for i, kind := range kinds {
		Register(kind, &concurrentTestSetup{
			kind:  kind,
			path:  filepath.Join(dir, fmt.Sprintf("device-%d.json", i)),
			ready: ready,
			start: start,
		})
		t.Cleanup(func() { delete(installers, kind) })
		install, ok := manager.OnDemandInstaller(kind)
		require.True(t, ok)
		installs = append(installs, install)
	}

	var readers sync.WaitGroup
	stop := make(chan struct{})
	readers.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
				manager.ListDevices()
				for _, kind := range kinds {
					manager.GetDevice(kind + "=foo")
				}
			}
		}
	})

	var writers errgroup.Group
	for _, install := range installs {
		writers.Go(func() error { return install(t.Context()) })
	}
	for range kinds {
		<-ready
	}
	close(start)
	err = writers.Wait()
	close(stop)
	readers.Wait()
	require.NoError(t, err)

	require.True(t, manager.GetDevice("configured.test/device=foo").AutoAllow)
	for _, kind := range kinds {
		require.True(t, manager.GetDevice(kind+"=foo").AutoAllow)
		_, ok := manager.OnDemandInstaller(kind)
		require.False(t, ok)
	}
	devices := manager.ListDevices()
	require.Len(t, devices, len(kinds))
	for _, device := range devices {
		require.True(t, device.AutoAllow)
		require.False(t, device.OnDemand)
	}
}

type concurrentTestSetup struct {
	kind  string
	path  string
	ready chan<- struct{}
	start <-chan struct{}
}

func (*concurrentTestSetup) Validate() error {
	return nil
}

func (s *concurrentTestSetup) Run(context.Context) error {
	err := os.WriteFile(s.path, fmt.Appendf(nil, `{
 "cdiVersion": "0.6.0",
 "kind": %q,
 "devices": [{"name": "foo", "containerEdits": {"env": ["TEST_DEVICE=1"]}}]
 }`, s.kind), 0600)
	// Publish both specs before either installer refreshes the cache.
	s.ready <- struct{}{}
	<-s.start
	return err
}
