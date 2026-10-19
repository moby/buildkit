//go:build !linux

package specconv

import (
	"runtime"

	"github.com/opencontainers/runtime-spec/specs-go"
	"github.com/pkg/errors"
)

// ToRootless converts spec to be compatible with "rootless" runc.
// * Remove /sys mount
// * Remove cgroups
//
// See docs/rootless.md for the supported runc revision.
func ToRootless(spec *specs.Spec) ([]string, error) {
	return nil, errors.Errorf("not implemented on %s", runtime.GOOS)
}

// ToRootlessWithCgroup is like ToRootless, but it keeps the resource limits
// and runs the container in cgroupsPath.
func ToRootlessWithCgroup(spec *specs.Spec, cgroupsPath string) ([]string, error) {
	return nil, errors.Errorf("not implemented on %s", runtime.GOOS)
}
