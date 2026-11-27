//go:build !windows

package gitutil

import "os"

// noConfigHome returns a path to use for HOME that prevents git from
// reading user-level configuration. On Unix, /dev/null works because
// git silently fails to read /dev/null/.gitconfig.
func noConfigHome() string {
	return os.DevNull
}

// noConfigGlobal returns a path to use for GIT_CONFIG_GLOBAL that
// prevents git from reading the global gitconfig.
func noConfigGlobal() string {
	return os.DevNull
}
