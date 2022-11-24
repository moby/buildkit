//go:build !windows
// +build !windows

package system

import (
	"path/filepath"
)

// CheckSystemDriveAndRemoveDriveLetter verifies that a path, if it includes a drive letter,
// is the system drive. This is a no-op on Linux.
func CheckSystemDriveAndRemoveDriveLetter(path string) (string, error) {
	return path, nil
}

func IsAbs(pth string) bool {
	return filepath.IsAbs(pth)
}
