package venus

import (
	"bytes"
	"context"
	"os"
	"strings"

	"github.com/moby/buildkit/solver/llbsolver/cdidevices"
	"github.com/pkg/errors"
	"golang.org/x/sys/unix"
)

const (
	cdiKind = "docker.com/gpu"
)

func init() {
	cdidevices.Register(cdiKind, &setup{})
}

type setup struct {
}

var _ cdidevices.Setup = &setup{}

func (s *setup) Validate() error {
	kVersion, err := getKernelVersion()
	if err != nil {
		return errors.Wrap(err, "failed to get kernel version")
	}
	if !strings.Contains(kVersion, "linuxkit") {
		return errors.Errorf("%s currently requires a linuxkit kernel", cdiKind)
	}

	_, err = os.Stat("/dev/dri")
	if err != nil {
		if os.IsNotExist(err) {
			return errors.Errorf("no DRI device found, make you use Docker VMM Hypervisor")
		}
		return errors.Wrap(err, "failed to check DRI device")
	}

	for _, dev := range []string{"renderD128", "card0"} {
		if _, err := os.Stat("/dev/dri/" + dev); err != nil {
			return errors.Wrapf(err, "failed to check DRI device %s", dev)
		}
	}
	return nil
}

func (s *setup) Run(ctx context.Context) error {
	if err := s.Validate(); err != nil {
		return err
	}

	const dt = `cdiVersion: "0.6.0"
kind: "docker.com/gpu"
annotations:
  cdi.device.name: "Virtio-GPU Venus (Docker Desktop)"
devices:
- name: venus
  containerEdits:
    deviceNodes:
    - path: /dev/dri/card0
    - path: /dev/dri/renderD128
`

	if err := os.MkdirAll("/etc/cdi", 0700); err != nil {
		return errors.Wrap(err, "failed to create /etc/cdi")
	}

	if err := os.WriteFile("/etc/cdi/venus.yaml", []byte(dt), 0600); err != nil {
		return errors.Wrap(err, "failed to write /etc/cdi/venus.yaml")
	}

	return nil
}

func getKernelVersion() (string, error) {
	var uts unix.Utsname
	if err := unix.Uname(&uts); err != nil {
		return "", err
	}
	return string(uts.Release[:bytes.IndexByte(uts.Release[:], 0)]), nil
}
