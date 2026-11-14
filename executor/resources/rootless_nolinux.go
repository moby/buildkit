//go:build !linux

package resources

import (
	"runtime"

	"github.com/pkg/errors"
)

// OwnCgroupControllers is only supported on Linux.
func OwnCgroupControllers() ([]string, error) {
	return nil, errors.Errorf("cgroups are not supported on %s", runtime.GOOS)
}
