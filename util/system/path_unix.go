//go:build !windows

package system

import "path/filepath"

// DefaultPathEnvWindows is windows style list of directories to search for
// executables. Each directory is separated from the next by a colon
// ';' character .
// On Windows, this is left empty and loaded from the registry hive
// during container run. Set here for consistency in the cross-platform builds.
const DefaultPathEnvWindows = "c:\\Windows\\System32;c:\\Windows;C:\\Windows\\System32\\WindowsPowerShell\\v1.0"

// IsAbsolutePath is just a wrapper that calls filepath.IsAbs.
// Has been added here just for symmetry with Windows.
func IsAbsolutePath(path string) bool {
	return filepath.IsAbs(path)
}

// GetAbsolutePath does nothing on non-Windows, just returns
// the same path.
func GetAbsolutePath(path string) string {
	return path
}
