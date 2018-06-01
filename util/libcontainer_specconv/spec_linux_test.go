// +build linux

package specconv

import (
	"reflect"
	"testing"

	"github.com/opencontainers/runc/libcontainer/configs/validate"
	"github.com/opencontainers/runc/libcontainer/specconv"
	"github.com/opencontainers/runc/libcontainer/user"
	"github.com/opencontainers/runtime-spec/specs-go"
)

func TestRootlessSpecconvValidate(t *testing.T) {
	specGen := func() *specs.Spec {
		spec := specconv.Example()
		spec.Root.Path = "/"
		return spec
	}
	cases := []struct {
		ctx                 RootlessContext
		opts                *RootlessOpts
		additionalValidator func(t *testing.T, s *specs.Spec)
	}{
		{
			ctx: RootlessContext{
				EUID: 0,
				EGID: 0,
			},
		},
		{
			ctx: RootlessContext{
				EUID: 4242,
				EGID: 4242,
			},
		},
		{
			ctx: RootlessContext{
				EUID: 4242,
				EGID: 4242,
				// empty subuid / subgid
			},
			opts: &RootlessOpts{
				MapSubUIDGID: true,
			},
		},
		{
			ctx: RootlessContext{
				EUID: 4242,
				EGID: 4242,
				SubUIDs: []user.SubID{
					{
						Name:  "dummy",
						SubID: 14242,
						Count: 65536,
					},
					{
						Name:  "dummy",
						SubID: 114242,
						Count: 65536,
					},
				},
				SubGIDs: []user.SubID{
					{
						Name:  "dummy",
						SubID: 14242,
						Count: 65536,
					},
					{
						Name:  "dummy",
						SubID: 114242,
						Count: 65536,
					},
				},
			},
			opts: &RootlessOpts{
				MapSubUIDGID: true,
			},
			additionalValidator: func(t *testing.T, s *specs.Spec) {
				expectedUIDMappings := []specs.LinuxIDMapping{
					{
						HostID:      4242,
						ContainerID: 0,
						Size:        1,
					},
					{
						HostID:      14242,
						ContainerID: 1,
						Size:        65536,
					},
					{
						HostID:      114242,
						ContainerID: 65537,
						Size:        65536,
					},
				}
				if !reflect.DeepEqual(expectedUIDMappings, s.Linux.UIDMappings) {
					t.Errorf("expected %#v, got %#v", expectedUIDMappings, s.Linux.UIDMappings)
				}
				expectedGIDMappings := expectedUIDMappings
				if !reflect.DeepEqual(expectedGIDMappings, s.Linux.GIDMappings) {
					t.Errorf("expected %#v, got %#v", expectedGIDMappings, s.Linux.GIDMappings)
				}
			},
		},
		{
			ctx: RootlessContext{
				EUID: 0,
				EGID: 0,
				UIDMap: []user.IDMap{
					{
						ID:       0,
						ParentID: 4242,
						Count:    1,
					},
					{
						ID:       1,
						ParentID: 231072,
						Count:    65536,
					},
				},
				GIDMap: []user.IDMap{
					{
						ID:       0,
						ParentID: 4242,
						Count:    1,
					},
					{
						ID:       1,
						ParentID: 231072,
						Count:    65536,
					},
				},
				InUserNS: true,
			},
			additionalValidator: func(t *testing.T, s *specs.Spec) {
				expectedUIDMappings := []specs.LinuxIDMapping{
					{
						HostID:      0,
						ContainerID: 0,
						Size:        1,
					},
					{
						HostID:      1,
						ContainerID: 1,
						Size:        65536,
					},
				}
				if !reflect.DeepEqual(expectedUIDMappings, s.Linux.UIDMappings) {
					t.Errorf("expected %#v, got %#v", expectedUIDMappings, s.Linux.UIDMappings)
				}
				expectedGIDMappings := expectedUIDMappings
				if !reflect.DeepEqual(expectedGIDMappings, s.Linux.GIDMappings) {
					t.Errorf("expected %#v, got %#v", expectedGIDMappings, s.Linux.GIDMappings)
				}
			},
		},
	}

	for _, c := range cases {
		spec := specGen()
		err := ToRootlessWithContext(c.ctx, spec, c.opts)
		if err != nil {
			t.Errorf("Couldn't convert a rootful spec to rootless: %v", err)
		}

		// t.Logf("%#v", spec)
		if c.additionalValidator != nil {
			c.additionalValidator(t, spec)
		}

		opts := &specconv.CreateOpts{
			CgroupName:       "ContainerID",
			UseSystemdCgroup: false,
			Spec:             spec,
			Rootless:         true,
		}

		config, err := specconv.CreateLibcontainerConfig(opts)
		if err != nil {
			t.Errorf("Couldn't create libcontainer config: %v", err)
		}

		validator := validate.New()
		if err := validator.Validate(config); err != nil {
			t.Errorf("Expected specconv to produce valid rootless container config: %v", err)
		}
	}
}
