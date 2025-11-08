//go:build !linux

package specconv

import (
	"fmt"
	"runtime"

	"github.com/opencontainers/runtime-spec/specs-go"
)

// ToRootless converts spec to be compatible with "rootless" runc.
// * Remove /sys mount
// * Remove cgroups
//
// See docs/rootless.md for the supported runc revision.
func ToRootless(spec *specs.Spec) error {
	return fmt.Errorf("not implemented on on %s", runtime.GOOS)
}
