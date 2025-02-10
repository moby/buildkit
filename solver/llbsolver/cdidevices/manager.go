package cdidevices

import (
	"context"
	"strings"

	"github.com/moby/locker"
	specs "github.com/opencontainers/runtime-spec/specs-go"
	"github.com/pkg/errors"
	"tags.cncf.io/container-device-interface/pkg/cdi"
)

var installers = map[string]Setup{}

type Setup interface {
	Validate() error
	Run(ctx context.Context) error
}

// Register registers new setup for a device.
func Register(name string, setup Setup) {
	installers[name] = setup
}

type Device struct {
	Name        string
	AutoAllow   bool
	OnDemand    bool
	Annotations map[string]string
}

type Manager struct {
	cache  *cdi.Cache
	locker *locker.Locker
}

func NewManager(cache *cdi.Cache) *Manager {
	return &Manager{
		cache:  cache,
		locker: locker.New(),
	}
}

func (m *Manager) ListDevices() []Device {
	devs := m.cache.ListDevices()
	out := make([]Device, 0, len(devs))
	kinds := make(map[string]struct{})
	for _, dev := range devs {
		kind, _, _ := strings.Cut(dev, "=")
		spec := m.cache.GetDevice(dev).GetSpec()
		out = append(out, Device{
			Name:        dev,
			AutoAllow:   true, // TODO
			Annotations: spec.Annotations,
		})
		kinds[kind] = struct{}{}
	}

	for k, setup := range installers {
		if _, ok := kinds[k]; ok {
			continue
		}
		if err := setup.Validate(); err != nil {
			continue
		}
		out = append(out, Device{
			Name:     k,
			OnDemand: true,
		})
	}

	return out
}

func (m *Manager) Refresh() error {
	return m.cache.Refresh()
}

func (m *Manager) InjectDevices(spec *specs.Spec, devs ...string) error {
	_, err := m.cache.InjectDevices(spec, devs...)
	return err
}

func (m *Manager) hasDevice(name string) bool {
	for _, d := range m.cache.ListDevices() {
		kind, _, _ := strings.Cut(d, "=")
		if kind == name {
			return true
		}
	}
	return false
}

func (m *Manager) OnDemandInstaller(name string) (func(context.Context) error, bool) {
	name, _, _ = strings.Cut(name, "=")

	installer, ok := installers[name]
	if !ok {
		return nil, false
	}

	if m.hasDevice(name) {
		return nil, false
	}

	return func(ctx context.Context) error {
		m.locker.Lock(name)
		defer m.locker.Unlock(name)

		if m.hasDevice(name) {
			return nil
		}

		if err := installer.Validate(); err != nil {
			return errors.Wrapf(err, "failed to find preconditions for %s device", name)
		}

		if err := installer.Run(ctx); err != nil {
			return errors.Wrapf(err, "failed to create %s device", name)
		}

		if err := m.cache.Refresh(); err != nil {
			return errors.Wrapf(err, "failed to refresh CDI cache")
		}

		return nil
	}, true
}
